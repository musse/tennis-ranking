package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed web/*
var assets embed.FS

type App struct { db *sql.DB; password, captchaSecret, captchaSite string }
type Ranking struct { ID int64 `json:"id"`; Name string `json:"name"`; Players []Player `json:"players"`; Active bool `json:"active"`; CreatedAt string `json:"createdAt"` }
type Player struct { ID int64 `json:"id"`; Name string `json:"name"` }
type Match struct { ID int64 `json:"id"`; RankingID int64 `json:"rankingId"`; WeekStart string `json:"weekStart"`; Players []Player `json:"players"`; Venue string `json:"venue"`; Court string `json:"court"`; PlayedAt string `json:"playedAt"`; Status string `json:"status"`; Sets []Set `json:"sets"` }
type Set struct { A int `json:"a"`; B int `json:"b"` }

func main() {
	_ = os.MkdirAll("data", 0755)
	db, err := sql.Open("sqlite", env("DATABASE_PATH", "data/rankings.db")); if err != nil { log.Fatal(err) }
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS rankings(id INTEGER PRIMARY KEY, name TEXT NOT NULL, active INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS players(id INTEGER PRIMARY KEY, ranking_id INTEGER NOT NULL REFERENCES rankings(id) ON DELETE CASCADE, name TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS matches(id INTEGER PRIMARY KEY, ranking_id INTEGER NOT NULL REFERENCES rankings(id) ON DELETE CASCADE, week_start TEXT NOT NULL, p1 INTEGER NOT NULL, p2 INTEGER NOT NULL, p3 INTEGER NOT NULL, p4 INTEGER NOT NULL, venue TEXT NOT NULL DEFAULT '', court TEXT NOT NULL DEFAULT '', played_at TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'scheduled', UNIQUE(ranking_id,week_start));
CREATE TABLE IF NOT EXISTS sets(id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE, set_no INTEGER NOT NULL, a INTEGER NOT NULL, b INTEGER NOT NULL);`); err != nil { log.Fatal(err) }
	app := &App{db: db, password: os.Getenv("APP_PASSWORD"), captchaSecret: os.Getenv("RECAPTCHA_SECRET_KEY"), captchaSite: os.Getenv("RECAPTCHA_SITE_KEY")}
	webFS, _ := fs.Sub(assets, "web")
	mux := http.NewServeMux(); mux.HandleFunc("/api/login", app.login); mux.HandleFunc("/api/logout",app.logout); mux.HandleFunc("/api/config",func(w http.ResponseWriter,r *http.Request){write(w,200,map[string]string{"captchaSiteKey":app.captchaSite})}); mux.Handle("/api/", app.auth(http.HandlerFunc(app.api))); mux.Handle("/", http.FileServer(http.FS(webFS)))
	log.Printf("Tennis ranking listening on :%s", env("PORT", "8080")); log.Fatal(http.ListenAndServe(":"+env("PORT", "8080"), mux))
}
func env(k, fallback string) string { if v := os.Getenv(k); v != "" { return v }; return fallback }
func write(w http.ResponseWriter, status int, v any) { w.Header().Set("Content-Type", "application/json"); w.WriteHeader(status); _ = json.NewEncoder(w).Encode(v) }
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost { write(w, 405, map[string]string{"error":"Method not allowed"}); return }
	var in struct { Password string `json:"password"`; Token string `json:"token"` }; if json.NewDecoder(r.Body).Decode(&in) != nil { write(w, 400, map[string]string{"error":"Invalid request"}); return }
	if a.password == "" { write(w, 503, map[string]string{"error":"APP_PASSWORD is not configured"}); return }
	if a.captchaSecret != "" && !verifyCaptcha(a.captchaSecret, in.Token) { write(w, 401, map[string]string{"error":"Captcha verification failed"}); return }
	if in.Password != a.password { write(w, 401, map[string]string{"error":"Incorrect password"}); return }
	http.SetCookie(w, &http.Cookie{Name:"tennis_session", Value:sessionToken(a.password), Path:"/", HttpOnly:true, SameSite:http.SameSiteStrictMode, Secure:r.TLS!=nil, MaxAge:60*60*24*14}); write(w,200,map[string]any{"ok":true})
}
func (a *App) logout(w http.ResponseWriter,r *http.Request){http.SetCookie(w,&http.Cookie{Name:"tennis_session",Value:"",Path:"/",HttpOnly:true,SameSite:http.SameSiteStrictMode,MaxAge:-1});write(w,200,map[string]bool{"ok":true})}
func sessionToken(p string) string { return fmt.Sprintf("%x", []byte(p+"-tennis-ranking")) }
func (a *App) auth(next http.Handler) http.Handler { return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){ c,e:=r.Cookie("tennis_session"); if e!=nil || a.password=="" || c.Value!=sessionToken(a.password) { write(w,401,map[string]string{"error":"Unauthorized"}); return }; next.ServeHTTP(w,r) }) }
func verifyCaptcha(secret, token string) bool { if token=="" { return false }; resp,err:=http.PostForm("https://www.google.com/recaptcha/api/siteverify",map[string][]string{"secret":{secret},"response":{token}}); if err!=nil{return false}; defer resp.Body.Close(); var out struct{ Success bool `json:"success"`; Score float64 `json:"score"`; Action string `json:"action"` }; if json.NewDecoder(resp.Body).Decode(&out)!=nil{return false}; return out.Success && out.Score>=0.5 && out.Action=="login" }
func (a *App) api(w http.ResponseWriter,r *http.Request) {
	parts:=strings.Split(strings.Trim(r.URL.Path,"/"),"/"); if len(parts)<2 { write(w,404,map[string]string{"error":"Not found"});return }
	if parts[1]=="rankings" { a.rankings(w,r,parts); return }; write(w,404,map[string]string{"error":"Not found"})
}
func (a *App) rankings(w http.ResponseWriter,r *http.Request,p []string) {
	if len(p)==2 { if r.Method=="GET" { rows,e:=a.db.Query("SELECT id,name,active,created_at FROM rankings ORDER BY id DESC"); if e!=nil { write(w,500,nil);return }; defer rows.Close(); out:=[]Ranking{}; for rows.Next(){var x Ranking;var active int; _=rows.Scan(&x.ID,&x.Name,&active,&x.CreatedAt);x.Active=active==1;x.Players,_=a.players(x.ID);out=append(out,x)};write(w,200,out);return }
		if r.Method=="POST" { var in struct{Name string `json:"name"`;Players []string `json:"players"`};if json.NewDecoder(r.Body).Decode(&in)!=nil||strings.TrimSpace(in.Name)==""||len(in.Players)<4 {write(w,400,map[string]string{"error":"Enter a ranking name and at least four players"});return}; tx,e:=a.db.Begin();if e!=nil{write(w,500,nil);return};defer tx.Rollback();res,e:=tx.Exec("INSERT INTO rankings(name,created_at) VALUES(?,?)",strings.TrimSpace(in.Name),time.Now().Format(time.RFC3339));if e!=nil{write(w,500,nil);return};id,_:=res.LastInsertId();seen:=map[string]bool{};for _,n:=range in.Players{n=strings.TrimSpace(n);if n!=""&&!seen[strings.ToLower(n)]{_,e=tx.Exec("INSERT INTO players(ranking_id,name) VALUES(?,?)",id,n);seen[strings.ToLower(n)]=true}};if len(seen)<4{write(w,400,map[string]string{"error":"Add at least four unique players"});return};if e=tx.Commit();e!=nil{write(w,500,nil);return};write(w,201,map[string]any{"id":id});return };write(w,405,nil);return }
	id,e:=strconv.ParseInt(p[2],10,64);if e!=nil{write(w,400,nil);return}
	if len(p)==3 { if r.Method=="GET" { var x Ranking;var active int;e=a.db.QueryRow("SELECT id,name,active,created_at FROM rankings WHERE id=?",id).Scan(&x.ID,&x.Name,&active,&x.CreatedAt);if e!=nil{write(w,404,nil);return};x.Active=active==1;x.Players,_=a.players(id);rows,_:=a.db.Query("SELECT id,ranking_id,week_start,venue,court,played_at,status FROM matches WHERE ranking_id=? ORDER BY week_start DESC",id);defer rows.Close();matches:=[]Match{};for rows.Next(){var m Match;var pids [4]int64;_ = rows.Scan(&m.ID,&m.RankingID,&m.WeekStart,&m.Venue,&m.Court,&m.PlayedAt,&m.Status);_ = a.db.QueryRow("SELECT p1,p2,p3,p4 FROM matches WHERE id=?",m.ID).Scan(&pids[0],&pids[1],&pids[2],&pids[3]);m.Players=[]Player{};for _,pid:=range pids{var pl Player;if a.db.QueryRow("SELECT id,name FROM players WHERE id=?",pid).Scan(&pl.ID,&pl.Name)==nil{m.Players=append(m.Players,pl)}};setRows,_:=a.db.Query("SELECT a,b FROM sets WHERE match_id=? ORDER BY set_no",m.ID);for setRows.Next(){var s Set;_ = setRows.Scan(&s.A,&s.B);m.Sets=append(m.Sets,s)};setRows.Close();matches=append(matches,m)};write(w,200,map[string]any{"ranking":x,"matches":matches});return }
		if r.Method=="DELETE"{_,e:=a.db.Exec("UPDATE rankings SET active=0 WHERE id=?",id);if e!=nil{write(w,500,nil);return};write(w,200,map[string]bool{"ok":true});return} }
	if len(p)==4&&p[3]=="matches"&&r.Method=="POST" { var in struct{WeekStart string `json:"weekStart"`};_ = json.NewDecoder(r.Body).Decode(&in);week,err:=time.Parse("2006-01-02",in.WeekStart);if err!=nil||week.Weekday()!=time.Saturday||week.Before(saturday(time.Now())){write(w,400,map[string]string{"error":"Choose a Saturday on or after this week"});return};var active int;if a.db.QueryRow("SELECT active FROM rankings WHERE id=?",id).Scan(&active)!=nil||active==0{write(w,404,nil);return};var count int;_ = a.db.QueryRow("SELECT COUNT(*) FROM matches WHERE ranking_id=? AND week_start=?",id,in.WeekStart).Scan(&count);if count>0{write(w,409,map[string]string{"error":"This week already has a match"});return};players,_:=a.players(id);if len(players)<4{write(w,400,map[string]string{"error":"A doubles match needs at least four players"});return};ids:=choose(players,id,in.WeekStart,a.db);res,err:=a.db.Exec("INSERT INTO matches(ranking_id,week_start,p1,p2,p3,p4) VALUES(?,?,?,?,?,?)",id,in.WeekStart,ids[0],ids[1],ids[2],ids[3]);if err!=nil{write(w,500,nil);return};mid,_:=res.LastInsertId();write(w,201,map[string]any{"id":mid});return }
	if len(p)==5&&p[3]=="matches" { mid,err:=strconv.ParseInt(p[4],10,64);if err!=nil{write(w,400,nil);return};if r.Method=="PATCH"{var in struct{Venue string `json:"venue"`;Court string `json:"court"`;PlayedAt string `json:"playedAt"`;Sets []Set `json:"sets"`};if json.NewDecoder(r.Body).Decode(&in)!=nil{write(w,400,nil);return};if in.Venue!="AP"&&in.Venue!="UP"{write(w,400,map[string]string{"error":"Venue must be AP or UP"});return};if in.Court==""{write(w,400,map[string]string{"error":"Court is required"});return};when,err:=time.Parse("2006-01-02T15:04",in.PlayedAt);if err!=nil{write(w,400,map[string]string{"error":"Choose a valid date and time"});return};var week string;_ = a.db.QueryRow("SELECT week_start FROM matches WHERE id=? AND ranking_id=?",mid,id).Scan(&week);ws,_:=time.Parse("2006-01-02",week);if when.Before(ws)||!when.Before(ws.AddDate(0,0,7)){write(w,400,map[string]string{"error":"Match must be between Saturday and Friday"});return};_,err=a.db.Exec("UPDATE matches SET venue=?,court=?,played_at=?,status='scheduled' WHERE id=? AND ranking_id=?",in.Venue,in.Court,in.PlayedAt,mid,id);if err!=nil{write(w,500,nil);return};if in.Sets!=nil{_,_=a.db.Exec("DELETE FROM sets WHERE match_id=?",mid);for i,s:=range in.Sets{_,_=a.db.Exec("INSERT INTO sets(match_id,set_no,a,b) VALUES(?,?,?,?)",mid,i+1,s.A,s.B)};_,_=a.db.Exec("UPDATE matches SET status='completed' WHERE id=?",mid)};write(w,200,map[string]bool{"ok":true});return};if r.Method=="DELETE"{_,err=a.db.Exec("DELETE FROM matches WHERE id=? AND ranking_id=?",mid,id);if err!=nil{write(w,500,nil)}else{write(w,200,map[string]bool{"ok":true})};return} }
	write(w,404,map[string]string{"error":"Not found"})
}
func (a *App) players(id int64)([]Player,error){rows,e:=a.db.Query("SELECT id,name FROM players WHERE ranking_id=? ORDER BY id",id);if e!=nil{return nil,e};defer rows.Close();out:=[]Player{};for rows.Next(){var p Player;_ = rows.Scan(&p.ID,&p.Name);out=append(out,p)};return out,nil}
func saturday(t time.Time)time.Time{d:=(int(t.Weekday())+1)%7;t=t.AddDate(0,0,-d);return time.Date(t.Year(),t.Month(),t.Day(),0,0,0,0,t.Location())}
func choose(ps []Player,rid int64,week string,db *sql.DB)[]int64{
	var played int;_ = db.QueryRow("SELECT COUNT(*) FROM matches WHERE ranking_id=?",rid).Scan(&played)
	type fixture [4]int64;fixtures:=[]fixture{}
	for i:=0;i<len(ps);i++{for j:=i+1;j<len(ps);j++{for k:=j+1;k<len(ps);k++{for l:=k+1;l<len(ps);l++{a,b,c,d:=ps[i].ID,ps[j].ID,ps[k].ID,ps[l].ID;fixtures=append(fixtures,fixture{a,b,c,d},fixture{a,c,b,d},fixture{a,d,b,c})}}}}
	if len(fixtures)==0{return nil};f:=fixtures[played%len(fixtures)];return []int64{f[0],f[1],f[2],f[3]}
}
