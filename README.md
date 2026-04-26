# Pluto

A Go HTTP server that generates M3U playlists from Pluto TV for use with Channels DVR Server.

Authenticates 12 independent sessions with Pluto TV to allow concurrent streaming (Pluto TV limits 1 stream per session). Channel data is cached for 30 minutes.

Build for later integration into tvproxy

## Endpoint

```
GET /playlist.m3u?user=N
```

`user` selects which auth session to use (1-12, defaults to 1). Each session has its own JWT, so each can stream independently.

## Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `PLUTO_USERNAME` | Yes | | Pluto TV account email |
| `PLUTO_PASSWORD` | Yes | | Pluto TV account password |
| `PORT` | No | `8080` | HTTP listen port |

## Running

### Docker

```bash
docker run -d --name pluto -p 8080:8080 \
  -e PLUTO_USERNAME='your@email.com' \
  -e PLUTO_PASSWORD='yourpassword' \
  gavinmcnair/pluto
```

### Local

```bash
export PLUTO_USERNAME='your@email.com'
export PLUTO_PASSWORD='yourpassword'
make run
```

## Channels DVR Setup

Add tuner playlists as separate Custom Channels sources, setting the stream limit to 1 for each:

```
http://<host>:8080/playlist.m3u?user=1
http://<host>:8080/playlist.m3u?user=2
...
```

All tuners share the same channel numbers, so Channels DVR will automatically failover between them.
