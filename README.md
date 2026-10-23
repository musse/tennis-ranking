# Quadra

A small, mobile-first tennis ranking manager for a group of friends. It runs as one Go container and stores data in SQLite.

## Run with Docker Compose

1. Copy `.env.example` to `.env` and set a shared `APP_PASSWORD`.
2. Add your Google reCAPTCHA v3 site and secret keys to `.env` for bot protection. When keys are configured, the server requires a valid token with a score of at least 0.5 at login.
3. Start the app with `docker compose up -d --build`.
4. Open `http://your-server:8080`.

The `quadra-data` volume stores the SQLite database across container updates. Put the app behind HTTPS on a public cloud VM so the shared password is protected in transit. The app can also run locally without reCAPTCHA keys.

## Current behavior

- One shared password; no user accounts. A successful login creates a 14-day HTTP-only session cookie.
- Create and manually end rankings. The complete player list is captured at creation and cannot be edited afterward.
- Generate a singles slate for each Saturday-to-Friday week, prioritizing opponents with fewer played head-to-head matches. Manually edited pairings count after their result is entered. Every player gets one opponent per round; with an odd roster, one player has a bye. The round-robin rotation breaks ties.
- Add the reserved date, time, venue, and court to each generated match, then enter the set scores later.
- Choose AP or UP, a court, and a match date/time in that week. AP offers courts 1, 2, 8, 9, and 10; UP offers courts 2, 3, 6, and 7. The available time slots are 07:00, 08:30, 17:30, 19:00, and 20:30. A match can also be marked without a reservation, with only its day and venue.
- Record best-of-three results with two regular sets and, when each player wins one set, a match tie-break to 10 points with a two-point margin. Match tie-break points do not count toward games in the standings.
- If play is interrupted after each player wins one set, record a draw: both players receive one draw in the standings and the played sets and games are retained. A match marked not played has no result and does not affect the standings; it can be reopened later. This does not assign a walkover automatically.
- Installable PWA shell with a service worker for static assets.

## Pages

- `/rankings` — ranking management.
- `/rankings/{id}/standings` — cumulative player standings.
- `/rankings/{id}/week` — current week's pairings, reservations, and results.
- `/rankings/{id}/history` — results and matches marked not played from previous weeks.

Each page has its own URL, so a selected ranking survives refresh and browser back/forward navigation.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `APP_PASSWORD` | required | Shared login password |
| `RECAPTCHA_SITE_KEY` | empty | Public Google v3 key, also enables browser challenge |
| `RECAPTCHA_SECRET_KEY` | empty | Private Google v3 verification key |
| `DATABASE_PATH` | `data/rankings.db` | SQLite database path |
| `PORT` | `8080` | HTTP listening port |

Both reCAPTCHA keys should be configured together. The secret key must remain on the server.
