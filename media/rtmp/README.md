# RTMP Streaming Stack

Ce répertoire regroupe tout ce qui concerne la diffusion vidéo (RTMP/HLS) sans le mélanger avec le serveur realtime/chat de l'API.

## Structure

- `mediamtx.yml` — configuration de base pour MediaMTX (ex-rtsp-simple-server).
- `docker-compose.yml` — composition facultative pour lancer MediaMTX.
- `docs/` — fiches pratiques et schémas optionnels.

## Prérequis

- Docker ou un binaire MediaMTX récent (>= 1.8).
- Des stream keys projetées par votre backend (l'API peut continuer à les gérer en mémoire ou via un endpoint dédié).

## Démarrage rapide

```bash
cd media/rtmp
REALTIME_CONFIG_PATH=../realtime/chat.yaml docker compose up -d
```

Ensuite, pointez OBS vers `rtmp://localhost:1935/live` avec le stream key souhaité. Les flux HLS seront disponibles sous `http://localhost:8888/live/<streamKey>/index.m3u8`.

> ⚠️ Le backend Go conserve l'authentification/tokenisation des streams. Les hooks MediaMTX peuvent interroger l'API (`/api/streams/{key}/start`) pour valider la session si nécessaire.

## Personnalisation

- Modifiez `mediamtx.yml` pour ajuster les sorties (HLS/DASH), les hooks d'auth, ou activer un enregistrement local.
- Ajoutez vos notes techniques dans `docs/`.
