# Stack Streaming

```
OBS (RTMP) ---> MediaMTX (RTMP/HLS) ---> Client Player (HLS)
                               \
                                +--> API GoLive (hooks, auth, chat realtime)
```

- **OBS** pousse sur `rtmp://<host>:1935/live` avec `streamKey`.
- **MediaMTX** convertit en HLS/DASH et relaie les hooks d'auth vers l'API.
- **API GoLive** continue de gérer chat, présence et modération.

Ajustez les ports selon votre infrastructure et pensez à sécuriser l’accès (TLS, firewall). Pour un déploiement prod, couplez MediaMTX avec un CDN ou reverse proxy.
