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
- Generate one doubles match for a Saturday-to-Friday week. Players are selected from the roster in a rotating order; a roster with an odd number leaves one player out of each match.
- Choose AP or UP, a court, and a match date/time in that week. The court list is a temporary 1–3 placeholder and should be replaced with the venue's actual courts. Time slots are currently entered directly; the requested preset schedule can be added once provided.
- Add set game scores; match scores are shown on the ranking page.
- Installable PWA shell with a service worker for static assets.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `APP_PASSWORD` | required | Shared login password |
| `RECAPTCHA_SITE_KEY` | empty | Public Google v3 key, also enables browser challenge |
| `RECAPTCHA_SECRET_KEY` | empty | Private Google v3 verification key |
| `DATABASE_PATH` | `data/rankings.db` | SQLite database path |
| `PORT` | `8080` | HTTP listening port |

Both reCAPTCHA keys should be configured together. The secret key must remain on the server.
