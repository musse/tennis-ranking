package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"math/bits"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed web/*
var assets embed.FS

type App struct {
	db                                   *sql.DB
	password, captchaSecret, captchaSite string
}
type Ranking struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Players   []Player `json:"players"`
	Active    bool     `json:"active"`
	CreatedAt string   `json:"createdAt"`
}
type Player struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
type Match struct {
	ID        int64    `json:"id"`
	RankingID int64    `json:"rankingId"`
	WeekStart string   `json:"weekStart"`
	Players   []Player `json:"players"`
	Venue     string   `json:"venue"`
	Court     string   `json:"court"`
	PlayedAt  string   `json:"playedAt"`
	Status    string   `json:"status"`
	Sets      []Set    `json:"sets"`
	p1        int64
	p2        int64
}
type Set struct {
	A    int    `json:"a"`
	B    int    `json:"b"`
	Kind string `json:"kind,omitempty"`
}

func main() {
	_ = os.MkdirAll("data", 0755)
	db, err := sql.Open("sqlite", env("DATABASE_PATH", "data/rankings.db"))
	if err != nil {
		log.Fatal(err)
	}
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS rankings(id INTEGER PRIMARY KEY, name TEXT NOT NULL, active INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS players(id INTEGER PRIMARY KEY, ranking_id INTEGER NOT NULL REFERENCES rankings(id) ON DELETE CASCADE, name TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS matches(id INTEGER PRIMARY KEY, ranking_id INTEGER NOT NULL REFERENCES rankings(id) ON DELETE CASCADE, week_start TEXT NOT NULL, p1 INTEGER NOT NULL, p2 INTEGER NOT NULL, p3 INTEGER NOT NULL DEFAULT 0, p4 INTEGER NOT NULL DEFAULT 0, venue TEXT NOT NULL DEFAULT '', court TEXT NOT NULL DEFAULT '', played_at TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'scheduled');
CREATE TABLE IF NOT EXISTS match_weeks(ranking_id INTEGER NOT NULL REFERENCES rankings(id) ON DELETE CASCADE, week_start TEXT NOT NULL, round_index INTEGER NOT NULL, PRIMARY KEY(ranking_id,week_start));
CREATE TABLE IF NOT EXISTS sets(id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE, set_no INTEGER NOT NULL, a INTEGER NOT NULL, b INTEGER NOT NULL, kind TEXT NOT NULL DEFAULT 'set');`); err != nil {
		log.Fatal(err)
	}
	if err = migrateMatches(db); err != nil {
		log.Fatal(err)
	}
	if err = ensureSetKind(db); err != nil {
		log.Fatal(err)
	}
	app := &App{db: db, password: os.Getenv("APP_PASSWORD"), captchaSecret: os.Getenv("RECAPTCHA_SECRET_KEY"), captchaSite: os.Getenv("RECAPTCHA_SITE_KEY")}
	webFS, _ := fs.Sub(assets, "web")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", app.login)
	mux.HandleFunc("/api/logout", app.logout)
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		write(w, 200, map[string]string{"captchaSiteKey": app.captchaSite})
	})
	mux.Handle("/api/", app.auth(http.HandlerFunc(app.api)))
	static := http.FileServer(http.FS(webFS))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if isRankingPage(r.URL.Path) {
			page, readErr := assets.ReadFile("web/index.html")
			if readErr != nil {
				http.Error(w, "Application page unavailable", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(page)
			return
		}
		static.ServeHTTP(w, r)
	})
	log.Printf("Tennis ranking listening on :%s", env("PORT", "8080"))
	log.Fatal(http.ListenAndServe(":"+env("PORT", "8080"), mux))
}
func isRankingPage(path string) bool {
	clean := strings.Trim(path, "/")
	if clean == "rankings" {
		return true
	}
	parts := strings.Split(clean, "/")
	if (len(parts) != 2 && len(parts) != 3) || parts[0] != "rankings" {
		return false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return false
	}
	return len(parts) == 2 || parts[2] == "standings" || parts[2] == "week" || parts[2] == "history"
}
func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func migrateMatches(db *sql.DB) error {
	var schema string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='matches'").Scan(&schema); err != nil {
		return err
	}
	if !strings.Contains(strings.ToLower(schema), "unique(ranking_id,week_start)") {
		return nil
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(context.Background(), "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	tx, err := conn.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	statements := []string{
		"ALTER TABLE sets RENAME TO sets_legacy",
		"ALTER TABLE matches RENAME TO matches_legacy",
		`CREATE TABLE matches(id INTEGER PRIMARY KEY, ranking_id INTEGER NOT NULL REFERENCES rankings(id) ON DELETE CASCADE, week_start TEXT NOT NULL, p1 INTEGER NOT NULL, p2 INTEGER NOT NULL, p3 INTEGER NOT NULL DEFAULT 0, p4 INTEGER NOT NULL DEFAULT 0, venue TEXT NOT NULL DEFAULT '', court TEXT NOT NULL DEFAULT '', played_at TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'scheduled')`,
		"CREATE TABLE sets(id INTEGER PRIMARY KEY, match_id INTEGER NOT NULL REFERENCES matches(id) ON DELETE CASCADE, set_no INTEGER NOT NULL, a INTEGER NOT NULL, b INTEGER NOT NULL, kind TEXT NOT NULL DEFAULT 'set')",
		"INSERT INTO matches SELECT * FROM matches_legacy",
		"INSERT INTO sets(id,match_id,set_no,a,b,kind) SELECT id,match_id,set_no,a,b,CASE WHEN set_no=3 THEN 'legacy_set' ELSE 'set' END FROM sets_legacy",
		"DROP TABLE sets_legacy",
		"DROP TABLE matches_legacy",
	}
	for _, statement := range statements {
		if _, err = tx.Exec(statement); err != nil {
			_ = tx.Rollback()
			_, _ = conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		_, _ = conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
		return err
	}
	_, err = conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
	return err
}

func ensureSetKind(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(sets)")
	if err != nil {
		return err
	}
	hasKind := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		if name == "kind" {
			hasKind = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if hasKind {
		return nil
	}
	if _, err = db.Exec("ALTER TABLE sets ADD COLUMN kind TEXT NOT NULL DEFAULT 'set'"); err != nil {
		return err
	}
	_, err = db.Exec("UPDATE sets SET kind='legacy_set' WHERE set_no=3")
	return err
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		write(w, 405, map[string]string{"error": "Method not allowed"})
		return
	}
	var in struct {
		Password string `json:"password"`
		Token    string `json:"token"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		write(w, 400, map[string]string{"error": "Invalid request"})
		return
	}
	if a.password == "" {
		write(w, 503, map[string]string{"error": "APP_PASSWORD is not configured"})
		return
	}
	if a.captchaSecret != "" && !verifyCaptcha(a.captchaSecret, in.Token) {
		write(w, 401, map[string]string{"error": "Captcha verification failed"})
		return
	}
	if in.Password != a.password {
		write(w, 401, map[string]string{"error": "Incorrect password"})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "tennis_session", Value: sessionToken(a.password), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: 60 * 60 * 24 * 14})
	write(w, 200, map[string]any{"ok": true})
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "tennis_session", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	write(w, 200, map[string]bool{"ok": true})
}
func sessionToken(p string) string { return fmt.Sprintf("%x", []byte(p+"-tennis-ranking")) }
func (a *App) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := r.Cookie("tennis_session")
		if e != nil || a.password == "" || c.Value != sessionToken(a.password) {
			write(w, 401, map[string]string{"error": "Unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
func verifyCaptcha(secret, token string) bool {
	if token == "" {
		return false
	}
	resp, err := http.PostForm("https://www.google.com/recaptcha/api/siteverify", map[string][]string{"secret": {secret}, "response": {token}})
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var out struct {
		Success bool    `json:"success"`
		Score   float64 `json:"score"`
		Action  string  `json:"action"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return false
	}
	return out.Success && out.Score >= 0.5 && out.Action == "login"
}
func (a *App) api(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 {
		write(w, 404, map[string]string{"error": "Not found"})
		return
	}
	if parts[1] == "rankings" {
		a.rankings(w, r, parts)
		return
	}
	write(w, 404, map[string]string{"error": "Not found"})
}
func (a *App) rankings(w http.ResponseWriter, r *http.Request, p []string) {
	if len(p) == 2 {
		if r.Method == "GET" {
			rows, e := a.db.Query("SELECT id,name,active,created_at FROM rankings ORDER BY id DESC")
			if e != nil {
				write(w, 500, nil)
				return
			}
			defer rows.Close()
			out := []Ranking{}
			for rows.Next() {
				var x Ranking
				var active int
				_ = rows.Scan(&x.ID, &x.Name, &active, &x.CreatedAt)
				x.Active = active == 1
				x.Players, _ = a.players(x.ID)
				out = append(out, x)
			}
			write(w, 200, out)
			return
		}
		if r.Method == "POST" {
			var in struct {
				Name    string   `json:"name"`
				Players []string `json:"players"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil || strings.TrimSpace(in.Name) == "" || len(in.Players) < 2 {
				write(w, 400, map[string]string{"error": "Enter a ranking name and at least two players"})
				return
			}
			tx, e := a.db.Begin()
			if e != nil {
				write(w, 500, nil)
				return
			}
			defer tx.Rollback()
			res, e := tx.Exec("INSERT INTO rankings(name,created_at) VALUES(?,?)", strings.TrimSpace(in.Name), time.Now().Format(time.RFC3339))
			if e != nil {
				write(w, 500, nil)
				return
			}
			id, _ := res.LastInsertId()
			seen := map[string]bool{}
			for _, n := range in.Players {
				n = strings.TrimSpace(n)
				if n != "" && !seen[strings.ToLower(n)] {
					_, e = tx.Exec("INSERT INTO players(ranking_id,name) VALUES(?,?)", id, n)
					seen[strings.ToLower(n)] = true
				}
			}
			if len(seen) < 2 {
				write(w, 400, map[string]string{"error": "Add at least two unique players"})
				return
			}
			if e = tx.Commit(); e != nil {
				write(w, 500, nil)
				return
			}
			write(w, 201, map[string]any{"id": id})
			return
		}
		write(w, 405, nil)
		return
	}
	id, e := strconv.ParseInt(p[2], 10, 64)
	if e != nil {
		write(w, 400, nil)
		return
	}
	if len(p) == 3 {
		if r.Method == "GET" {
			a.rankingDetail(w, id)
			return
		}
		if r.Method == "DELETE" {
			_, e := a.db.Exec("UPDATE rankings SET active=0 WHERE id=?", id)
			if e != nil {
				write(w, 500, nil)
				return
			}
			write(w, 200, map[string]bool{"ok": true})
			return
		}
	}
	if len(p) == 4 && p[3] == "matches" && r.Method == "POST" {
		var in struct {
			WeekStart string `json:"weekStart"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		week, err := time.ParseInLocation("2006-01-02", in.WeekStart, time.Local)
		if err != nil || week.Weekday() != time.Saturday || week.Before(saturday(time.Now())) {
			write(w, 400, map[string]string{"error": "Escolha um sábado a partir desta semana."})
			return
		}
		var active int
		if a.db.QueryRow("SELECT active FROM rankings WHERE id=?", id).Scan(&active) != nil || active == 0 {
			write(w, 404, nil)
			return
		}
		var existing int
		_ = a.db.QueryRow("SELECT COUNT(*) FROM matches WHERE ranking_id=? AND week_start=?", id, in.WeekStart).Scan(&existing)
		if existing > 0 {
			write(w, 409, map[string]string{"error": "Confrontos desta semana já foram gerados"})
			return
		}
		players, _ := a.players(id)
		if len(players) < 2 {
			write(w, 400, map[string]string{"error": "O ranking precisa de pelo menos dois jogadores."})
			return
		}
		var round int
		_ = a.db.QueryRow("SELECT COUNT(DISTINCT week_start) FROM matches WHERE ranking_id=?", id).Scan(&round)
		matchupCounts, err := a.matchupCounts(id)
		if err != nil {
			write(w, 500, nil)
			return
		}
		fixtures, bye := roundRobin(players, round, matchupCounts)
		tx, err := a.db.Begin()
		if err != nil {
			write(w, 500, nil)
			return
		}
		defer tx.Rollback()
		if _, err = tx.Exec("INSERT INTO match_weeks(ranking_id,week_start,round_index) VALUES(?,?,?)", id, in.WeekStart, round); err != nil {
			write(w, 409, map[string]string{"error": "Confrontos desta semana já foram gerados"})
			return
		}
		created := []int64{}
		for _, pair := range fixtures {
			res, insertErr := tx.Exec("INSERT INTO matches(ranking_id,week_start,p1,p2) VALUES(?,?,?,?)", id, in.WeekStart, pair[0], pair[1])
			if insertErr != nil {
				write(w, 500, nil)
				return
			}
			matchID, _ := res.LastInsertId()
			created = append(created, matchID)
		}
		if err = tx.Commit(); err != nil {
			write(w, 500, nil)
			return
		}
		write(w, 201, map[string]any{"matchIds": created, "bye": bye})
		return
	}
	if len(p) == 5 && p[3] == "matches" {
		mid, err := strconv.ParseInt(p[4], 10, 64)
		if err != nil {
			write(w, 400, nil)
			return
		}
		if r.Method == "PATCH" {
			var in struct {
				Venue    string   `json:"venue"`
				Court    string   `json:"court"`
				PlayedAt string   `json:"playedAt"`
				Sets     *[]Set   `json:"sets"`
				Players  *[]int64 `json:"players"`
			}
			if json.NewDecoder(r.Body).Decode(&in) != nil {
				write(w, 400, nil)
				return
			}
			var week string
			if a.db.QueryRow("SELECT week_start FROM matches WHERE id=? AND ranking_id=?", mid, id).Scan(&week) != nil {
				write(w, 404, nil)
				return
			}
			if in.Players != nil {
				if len(*in.Players) != 2 || (*in.Players)[0] == (*in.Players)[1] {
					write(w, 400, map[string]string{"error": "Escolha dois jogadores diferentes."})
					return
				}
				var status string
				if a.db.QueryRow("SELECT status FROM matches WHERE id=? AND ranking_id=?", mid, id).Scan(&status) != nil {
					write(w, 404, nil)
					return
				}
				if status == "completed" {
					write(w, 400, map[string]string{"error": "Não é possível alterar os jogadores de uma partida com resultado lançado."})
					return
				}
				for _, playerID := range *in.Players {
					var exists int
					if a.db.QueryRow("SELECT COUNT(*) FROM players WHERE id=? AND ranking_id=?", playerID, id).Scan(&exists) != nil || exists == 0 {
						write(w, 400, map[string]string{"error": "Os jogadores devem pertencer a este ranking."})
						return
					}
				}
				if _, err := a.db.Exec("UPDATE matches SET p1=?,p2=? WHERE id=? AND ranking_id=?", (*in.Players)[0], (*in.Players)[1], mid, id); err != nil {
					write(w, 500, nil)
					return
				}
			}
			if in.Venue != "" || in.Court != "" || in.PlayedAt != "" {
				if (in.Venue != "AP" && in.Venue != "UP") || in.PlayedAt == "" {
					write(w, 400, map[string]string{"error": "Escolha o dia e a sede."})
					return
				}
				dayText := in.PlayedAt
				var when time.Time
				if in.Court == "" {
					if len(in.PlayedAt) != len("2006-01-02") {
						write(w, 400, map[string]string{"error": "Sem reserva, informe apenas o dia da partida."})
						return
					}
					when, err = time.ParseInLocation("2006-01-02", in.PlayedAt, time.Local)
				} else {
					when, err = time.ParseInLocation("2006-01-02T15:04", in.PlayedAt, time.Local)
					dayText = when.Format("2006-01-02")
				}
				if err != nil {
					write(w, 400, map[string]string{"error": "Escolha uma data e um horário disponíveis."})
					return
				}
				if in.Court != "" {
					allowedTimes := map[string]bool{"07:00": true, "08:30": true, "17:30": true, "19:00": true, "20:30": true}
					if !allowedTimes[when.Format("15:04")] {
						write(w, 400, map[string]string{"error": "Escolha um dos horários disponíveis."})
						return
					}
				}
				ws, _ := time.ParseInLocation("2006-01-02", week, time.Local)
				day, _ := time.ParseInLocation("2006-01-02", dayText, time.Local)
				if day.Before(ws) || !day.Before(ws.AddDate(0, 0, 7)) {
					write(w, 400, map[string]string{"error": "A partida deve acontecer entre sábado e sexta-feira."})
					return
				}
				if _, err = a.db.Exec("UPDATE matches SET venue=?,court=?,played_at=? WHERE id=? AND ranking_id=?", in.Venue, in.Court, in.PlayedAt, mid, id); err != nil {
					write(w, 500, nil)
					return
				}
			}
			if in.Sets != nil {
				if len(*in.Sets) < 2 || len(*in.Sets) > 3 {
					write(w, 400, map[string]string{"error": "Informe os dois sets e, se necessário, o match tie-break."})
					return
				}
				var playedAt string
				if err := a.db.QueryRow("SELECT played_at FROM matches WHERE id=?", mid).Scan(&playedAt); err != nil || playedAt == "" {
					write(w, 400, map[string]string{"error": "Adicione a reserva antes de lançar o resultado."})
					return
				}
				setWins := [2]int{}
				setKinds := make([]string, len(*in.Sets))
				for i, set := range *in.Sets {
					if set.A < 0 || set.B < 0 || set.A == set.B {
						write(w, 400, map[string]string{"error": "Os placares devem ser diferentes e não negativos."})
						return
					}
					winner := 0
					if set.B > set.A {
						winner = 1
					}
					if i < 2 {
						if !validRegularSet(set.A, set.B) {
							write(w, 400, map[string]string{"error": "Os dois primeiros sets devem terminar em 6 games, ou 7 games com tie-break."})
							return
						}
						setWins[winner]++
						setKinds[i] = "set"
					} else if set.Kind == "legacy_set" {
						if setWins != [2]int{1, 1} || !validRegularSet(set.A, set.B) {
							write(w, 400, map[string]string{"error": "O placar do terceiro set antigo é inválido."})
							return
						}
						setKinds[i] = "legacy_set"
					} else {
						if setWins != [2]int{1, 1} {
							write(w, 400, map[string]string{"error": "O terceiro set só é disputado quando cada jogador vence um dos dois primeiros sets."})
							return
						}
						if !validMatchTiebreak(set.A, set.B) {
							write(w, 400, map[string]string{"error": "O match tie-break deve chegar a pelo menos 10 pontos, com diferença mínima de 2."})
							return
						}
						setKinds[i] = "match_tiebreak"
					}
				}
				if len(*in.Sets) == 2 && setWins[0] == setWins[1] {
					write(w, 400, map[string]string{"error": "Como cada jogador venceu um set, informe o match tie-break do terceiro set."})
					return
				}
				_, _ = a.db.Exec("DELETE FROM sets WHERE match_id=?", mid)
				for i, s := range *in.Sets {
					_, _ = a.db.Exec("INSERT INTO sets(match_id,set_no,a,b,kind) VALUES(?,?,?,?,?)", mid, i+1, s.A, s.B, setKinds[i])
				}
				_, _ = a.db.Exec("UPDATE matches SET status='completed' WHERE id=?", mid)
			}
			write(w, 200, map[string]bool{"ok": true})
			return
		}
		if r.Method == "DELETE" {
			_, err = a.db.Exec("DELETE FROM matches WHERE id=? AND ranking_id=?", mid, id)
			if err != nil {
				write(w, 500, nil)
			} else {
				write(w, 200, map[string]bool{"ok": true})
			}
			return
		}
	}
	write(w, 404, map[string]string{"error": "Not found"})
}
func (a *App) rankingDetail(w http.ResponseWriter, id int64) {
	var ranking Ranking
	var active int
	if err := a.db.QueryRow("SELECT id,name,active,created_at FROM rankings WHERE id=?", id).Scan(&ranking.ID, &ranking.Name, &active, &ranking.CreatedAt); err != nil {
		write(w, 404, nil)
		return
	}
	ranking.Active = active == 1
	ranking.Players, _ = a.players(id)
	rows, err := a.db.Query("SELECT id,ranking_id,week_start,p1,p2,venue,court,played_at,status FROM matches WHERE ranking_id=? ORDER BY week_start DESC,id", id)
	if err != nil {
		write(w, 500, nil)
		return
	}
	matches := []Match{}
	for rows.Next() {
		var m Match
		var p1, p2 int64
		if err = rows.Scan(&m.ID, &m.RankingID, &m.WeekStart, &p1, &p2, &m.Venue, &m.Court, &m.PlayedAt, &m.Status); err != nil {
			_ = rows.Close()
			write(w, 500, nil)
			return
		}
		matches = append(matches, matchesWithPair(m, p1, p2))
	}
	_ = rows.Close()
	for i := range matches {
		matches[i].Players = []Player{}
		for _, pid := range matchPlayerIDs(matches[i]) {
			var pl Player
			if a.db.QueryRow("SELECT id,name FROM players WHERE id=?", pid).Scan(&pl.ID, &pl.Name) == nil {
				matches[i].Players = append(matches[i].Players, pl)
			}
		}
		setRows, e := a.db.Query("SELECT a,b,kind FROM sets WHERE match_id=? ORDER BY set_no", matches[i].ID)
		if e == nil {
			for setRows.Next() {
				var s Set
				_ = setRows.Scan(&s.A, &s.B, &s.Kind)
				matches[i].Sets = append(matches[i].Sets, s)
			}
			_ = setRows.Close()
		}
	}
	write(w, 200, map[string]any{"ranking": ranking, "matches": matches})
}

func validRegularSet(a, b int) bool {
	winner, loser := a, b
	if b > a {
		winner, loser = b, a
	}
	return (winner == 6 && loser <= 4) || (winner == 7 && (loser == 5 || loser == 6))
}

func validMatchTiebreak(a, b int) bool {
	winner, loser := a, b
	if b > a {
		winner, loser = b, a
	}
	return winner >= 10 && winner-loser >= 2
}

// Match participants are attached while reading rows to avoid a query on an open SQLite cursor.
func matchesWithPair(m Match, p1, p2 int64) Match { m.p1 = p1; m.p2 = p2; return m }
func matchPlayerIDs(m Match) [2]int64             { return [2]int64{m.p1, m.p2} }
func (a *App) players(id int64) ([]Player, error) {
	rows, e := a.db.Query("SELECT id,name FROM players WHERE ranking_id=? ORDER BY id", id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Player{}
	for rows.Next() {
		var p Player
		_ = rows.Scan(&p.ID, &p.Name)
		out = append(out, p)
	}
	return out, nil
}
func saturday(t time.Time) time.Time {
	d := (int(t.Weekday()) + 1) % 7
	t = t.AddDate(0, 0, -d)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func (a *App) matchupCounts(rankingID int64) (map[[2]int64]int, error) {
	rows, err := a.db.Query(`SELECT CASE WHEN p1<p2 THEN p1 ELSE p2 END,
CASE WHEN p1<p2 THEN p2 ELSE p1 END, COUNT(*)
FROM matches WHERE ranking_id=? AND status='completed' GROUP BY 1,2`, rankingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[[2]int64]int)
	for rows.Next() {
		var p1, p2 int64
		var count int
		if err := rows.Scan(&p1, &p2, &count); err != nil {
			return nil, err
		}
		counts[[2]int64{p1, p2}] = count
	}
	return counts, rows.Err()
}

func roundRobin(ps []Player, round int, matchupCounts map[[2]int64]int) ([][2]int64, *Player) {
	ring := make([]int64, len(ps))
	for i, p := range ps {
		ring[i] = p.ID
	}
	if len(ring)%2 == 1 {
		ring = append(ring, 0)
	}
	if len(ring) < 2 {
		return nil, nil
	}
	rounds := len(ring) - 1
	for r := 0; r < round%rounds; r++ {
		last := ring[len(ring)-1]
		copy(ring[2:], ring[1:len(ring)-1])
		ring[1] = last
	}
	canonical := make(map[[2]int64]bool)
	var canonicalBye int64
	for i := 0; i < len(ring)/2; i++ {
		a, b := ring[i], ring[len(ring)-1-i]
		if a == 0 || b == 0 {
			canonicalBye = a
			if canonicalBye == 0 {
				canonicalBye = b
			}
		} else {
			canonical[[2]int64{min64(a, b), max64(a, b)}] = true
		}
	}

	// Minimize the total number of completed head-to-head matches in this slate.
	// The canonical round-robin rotation breaks ties to preserve the old sequence.
	n := len(ring)
	if n > 20 {
		return greedyRoundRobin(ps, ring, canonical, canonicalBye, matchupCounts)
	}
	scale := n + 1
	edgeCost := func(i, j int) int {
		a, b := ring[i], ring[j]
		if a == 0 || b == 0 {
			if a == canonicalBye || b == canonicalBye {
				return 0
			}
			return 1
		}
		count := matchupCounts[[2]int64{min64(a, b), max64(a, b)}]
		tie := 1
		if canonical[[2]int64{min64(a, b), max64(a, b)}] {
			tie = 0
		}
		return count*scale + tie
	}
	stateCount := 1 << n
	memo, seen := make([]int, stateCount), make([]bool, stateCount)
	var solve func(uint64) int
	solve = func(mask uint64) int {
		if mask == 0 {
			return 0
		}
		idx := int(mask)
		if seen[idx] {
			return memo[idx]
		}
		i := bits.TrailingZeros64(mask)
		rest := mask &^ (uint64(1) << i)
		best := int(^uint(0) >> 1)
		for j := i + 1; j < n; j++ {
			bit := uint64(1) << j
			if rest&bit == 0 {
				continue
			}
			if cost := edgeCost(i, j) + solve(rest&^bit); cost < best {
				best = cost
			}
		}
		seen[idx], memo[idx] = true, best
		return best
	}
	mask := (uint64(1) << n) - 1
	pairs := make([][2]int64, 0, len(ps)/2)
	var bye *Player
	for mask != 0 {
		i := bits.TrailingZeros64(mask)
		rest := mask &^ (uint64(1) << i)
		for j := i + 1; j < n; j++ {
			bit := uint64(1) << j
			if rest&bit == 0 || edgeCost(i, j)+solve(rest&^bit) != solve(mask) {
				continue
			}
			if ring[i] == 0 || ring[j] == 0 {
				id := ring[i]
				if id == 0 {
					id = ring[j]
				}
				for k := range ps {
					if ps[k].ID == id {
						player := ps[k]
						bye = &player
						break
					}
				}
			} else {
				pairs = append(pairs, [2]int64{ring[i], ring[j]})
			}
			mask = rest &^ bit
			break
		}
	}
	return pairs, bye
}

func greedyRoundRobin(ps []Player, ring []int64, canonical map[[2]int64]bool, canonicalBye int64, counts map[[2]int64]int) ([][2]int64, *Player) {
	used := make([]bool, len(ring))
	pairs := make([][2]int64, 0, len(ps)/2)
	var bye *Player
	scale := len(ring) + 1
	for remaining := len(ring); remaining > 0; remaining -= 2 {
		bestI, bestJ, bestCost := -1, -1, int(^uint(0)>>1)
		for i := range ring {
			if used[i] {
				continue
			}
			for j := i + 1; j < len(ring); j++ {
				if used[j] {
					continue
				}
				cost := 0
				a, b := ring[i], ring[j]
				if a == 0 || b == 0 {
					if a != canonicalBye && b != canonicalBye {
						cost = 1
					}
				} else {
					cost = counts[[2]int64{min64(a, b), max64(a, b)}]*scale + 1
					if canonical[[2]int64{min64(a, b), max64(a, b)}] {
						cost--
					}
				}
				if cost < bestCost {
					bestI, bestJ, bestCost = i, j, cost
				}
			}
		}
		used[bestI], used[bestJ] = true, true
		a, b := ring[bestI], ring[bestJ]
		if a == 0 || b == 0 {
			id := a
			if id == 0 {
				id = b
			}
			for i := range ps {
				if ps[i].ID == id {
					player := ps[i]
					bye = &player
					break
				}
			}
		} else {
			pairs = append(pairs, [2]int64{a, b})
		}
	}
	return pairs, bye
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
