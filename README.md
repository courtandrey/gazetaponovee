# Газета Поновее

A small news site for a fictional outlet: chronological feed with pinned stories and a
«Срочно» strip, an open submission form with image upload, an admin desk for moderation,
and an election-night section (electoral map of the Nine States, score to 56 of 111, Senate board).

One Go binary (~14 MB image) with SQLite and uploads in a Docker volume, behind Caddy for HTTPS.
No JavaScript, no external requests: fonts are bundled.

## Run

```sh
cp .env.example .env      # set DOMAIN (DNS must point here; ports 80/443 open)
docker compose up -d --build
```

Create an admin (or change a password) — this only works from the server itself:

```sh
docker compose exec app /gazeta admin ИМЯ 'ПАРОЛЬ'
# or through the internal API, bound to 127.0.0.1 only:
curl -X POST localhost:9090/internal/admins -d '{"username":"ИМЯ","password":"ПАРОЛЬ"}'
```

Passwords are 8–72 characters. Then sign in at `/admin/login` (link in the footer).

## What the admin can do

- **Материалы** — review submissions, edit text/byline/rubric/images, publish, unpublish, reject,
  delete, pin to the top of the front page, mark as «Срочно». Write articles directly.
- **Выборы** — show/hide the section (hidden by default; admins can preview it at `/elections`),
  set candidates with colours, call or un-call each state, track Senate races, reset results.

## Configuration (`.env`)

| Variable | Default | Meaning |
|---|---|---|
| `DOMAIN` | `localhost` | Site domain; Caddy obtains the certificate itself |
| `TZ` | `Europe/Moscow` | Time zone for dates |
| `SITE_MOTTO` | built-in | Line under the masthead |
| `MAX_IMAGE_MB` / `MAX_IMAGES` | `5` / `6` | Upload limits per submission |
| `INTERNAL_PORT` | `9090` | Host loopback port of the internal API |
| `COOKIE_SECURE` | `true` | Set `false` only when serving plain HTTP |
| `HTTP_PORT` / `HTTPS_PORT` | `80` / `443` | Caddy's host ports |

## Data and backup

Everything lives in the `data` volume: `gazeta.db` (SQLite) and `uploads/`.

```sh
docker compose stop app
docker run --rm -v gazetaponovee_data:/data -v "$PWD":/out alpine tar czf /out/backup.tgz -C /data .
docker compose start app
```

## Development

```sh
go test ./...                                   # needs Go 1.26+
python tools/trace_map.py map.jpg mapdata.json  # re-trace state outlines (opencv, numpy)
```

Electoral votes and map label positions are in `elections.go`; rubrics are in `text.go`.
Article markup is a small Markdown subset — see the «Как оформить текст» hint on the form.
