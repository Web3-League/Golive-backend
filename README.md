# GoLive Platform – Chat Temps Réel & Streaming

GoLive est une plateforme légère en Go qui combine un serveur de chat temps réel avec une stack de streaming RTMP/HLS. L'état du chat est maintenu en mémoire par défaut (redémarrage = reset), avec la possibilité de charger une configuration YAML et d'exposer des endpoints d'administration.

## 🚀 Fonctionnalités

- **Chat temps réel** via WebSockets avec support des channels
- **API REST** complète pour la gestion des channels et modération
- **Streaming RTMP/HLS** intégré avec MediaMTX
- **Authentification JWT** optionnelle
- **Configuration YAML** pour les namespaces et ACL
- **Modération** avancée (ban, timeout, suppression de messages)

---

## 📋 Table des matières

- [Installation](#installation)
- [Configuration](#configuration)
- [API WebSocket](#api-websocket)
- [API REST](#api-rest)
- [Streaming](#streaming)
- [Authentification](#authentification)
- [Développement](#développement)

---

## 🔧 Installation

### Prérequis

- Go 1.19+
- Docker & Docker Compose (pour le streaming)

### Démarrage rapide

```bash
# Cloner le projet
git clone <repository-url>
cd golive

# Variables d'environnement
export JWT_SECRET=your-secret-key-here
export REALTIME_CONFIG_PATH=./config/realtime.yaml  # Optionnel

# Compilation et lancement
go build ./...
./golive

# Ou directement
go run api/cmd/server/main.go
```

Le serveur démarre par défaut sur `http://localhost:8080`

---

## ⚙️ Configuration

### Configuration YAML (optionnelle)

Créez un fichier `config/realtime.yaml` :

```yaml
namespaces:
  - name: chat
    max_clients: 1000
    allow_publish: true

channels:
  - pattern: "chat:stream_*"
    namespace: chat
    private: true
    allowed_roles: ["streamer", "moderator"]
    allowed_users: ["user_123", "user_456"]
    max_history: 100

  - pattern: "public:*"
    namespace: chat
    private: false
    max_history: 50


# Configuration des proxies RPC (optionnel)
rpc_proxies:
  - method: "user_stats"
    url: "http://localhost:3000/api/rpc/user_stats"
```

### Variables d'environnement

| Variable | Description | Défaut |
|----------|-------------|---------|
| `JWT_SECRET` | Clé secrète pour signer les JWT | `change-me` |
| `REALTIME_CONFIG_PATH` | Chemin vers le fichier de config YAML | - |
| `PORT` | Port d'écoute du serveur | `8080` |
| `LOG_LEVEL` | Niveau de log (debug, info, warn, error) | `info` |

---

## 🔌 API WebSocket

### Connexion

```javascript
const ws = new WebSocket('ws://localhost:8080/api/ws');

// Avec authentification JWT
const ws = new WebSocket('ws://localhost:8080/api/ws', [], {
  headers: {
    'Authorization': 'Bearer your-jwt-token'
  }
});
```

### Commandes disponibles

#### S'abonner à un channel

```json
{
  "type": "subscribe",
  "channel": "chat:stream_abc"
}
```

#### Publier un message

```json
{
  "type": "publish",
  "channel": "chat:stream_abc",
  "data": {
    "content": "Hello world!",
    "timestamp": "2024-01-15T10:30:00Z"
  }
}
```

#### Obtenir la présence

```json
{
  "type": "presence",
  "channel": "chat:stream_abc"
}
```

#### Appel RPC

```json
{
  "type": "rpc",
  "data": {
    "method": "stats"
  }
}
```

**Réponse RPC :**

```json
{
  "type": "rpc",
  "data": {
    "method": "stats",
    "result": {
      "total_channels": 3,
      "total_clients": 25,
      "uptime": "2h30m"
    }
  }
}
```

---

## 🌐 API REST

### Endpoints principaux

| Méthode | Route | Description |
|---------|-------|-------------|
| `GET` | `/api/realtime/channels` | Liste des channels actifs |
| `GET` | `/api/realtime/channels/{channel}` | Infos d'un channel |
| `GET` | `/api/realtime/channels/{channel}/history` | Historique des messages |
| `GET` | `/api/realtime/channels/{channel}/presence` | Utilisateurs présents |
| `POST` | `/api/realtime/channels/{channel}/publish` | Publier un message |
| `POST` | `/api/realtime/channels/{channel}/broadcast` | Message système |

### Modération

| Méthode | Route | Description |
|---------|-------|-------------|
| `DELETE` | `/api/realtime/channels/{channel}/messages/{id}` | Supprimer un message |
| `POST` | `/api/realtime/channels/{channel}/users/{id}/timeout` | Timeout utilisateur |
| `POST` | `/api/realtime/channels/{channel}/users/{id}/ban` | Bannir utilisateur |
| `DELETE` | `/api/realtime/channels/{channel}/users/{id}/ban` | Débannir utilisateur |

### Gestion des accès

| Méthode | Route | Description |
|---------|-------|-------------|
| `PUT` | `/api/realtime/channels/{channel}/settings` | Mettre à jour les paramètres |
| `POST` | `/api/realtime/channels/{channel}/access` | Ajouter des utilisateurs autorisés |
| `DELETE` | `/api/realtime/channels/{channel}/access/{id}` | Retirer un accès |

### Exemples d'utilisation

#### Publier un message via REST

```bash
curl -X POST http://localhost:8080/api/realtime/channels/chat:stream_abc/publish \
  -H "Authorization: Bearer your-jwt-token" \
  -H "Content-Type: application/json" \
  -d '{
    "data": {
      "content": "Message from REST API",
      "type": "system"
    }
  }'
```

#### Obtenir l'historique

```bash
curl http://localhost:8080/api/realtime/channels/chat:stream_abc/history?limit=50
```

---

## 📺 Streaming (optionnel)

Le module de streaming utilise MediaMTX pour gérer les flux RTMP et la conversion HLS/DASH.

### Configuration

Tout le volet streaming se trouve dans `media/rtmp/` :

- `mediamtx.yml` : Configuration MediaMTX
- `docker-compose.yml` : Déploiement rapide
- `docs/overview.md` : Documentation détaillée

### Démarrage

```bash
cd media/rtmp
docker compose up -d
```

### Utilisation

#### Streaming depuis OBS

- **URL RTMP** : `rtmp://localhost:1935/live`
- **Stream Key** : `your-stream-key`

#### Lecture des flux

- **HLS** : `http://localhost:8888/live/<streamKey>/index.m3u8`
- **DASH** : `http://localhost:8888/live/<streamKey>/index.mpd`

### Intégration avec l'API

L'API GoLive peut valider les stream keys et synchroniser l'état des streams avec le chat :

```bash
# Webhook MediaMTX vers GoLive
curl -X POST http://localhost:8080/api/streaming/webhook \
  -H "Content-Type: application/json" \
  -d '{
    "event": "stream_started",
    "stream_key": "abc123",
    "user_id": "streamer_456"
  }'
```

---

## 🔐 Authentification

### JWT Configuration

L'authentification utilise des tokens JWT avec la structure suivante :

```json
{
  "sub": "user_123",
  "username": "alice",
  "role": "streamer",
  "exp": 1642694400,
  "permissions": ["chat.publish", "stream.create"]
}
```

### Générer un token

```javascript
// Exemple Node.js
const jwt = require('jsonwebtoken');

const token = jwt.sign({
  sub: 'user_123',
  username: 'alice',
  role: 'streamer',
  permissions: ['chat.publish', 'stream.create']
}, 'your-jwt-secret', {
  expiresIn: '24h'
});
```

### Middleware d'authentification

- `mw.Auth` : Authentification obligatoire
- `mw.OptionalAuth` : Authentification optionnelle

---

## 🛠️ Développement

### Structure du projet

```
.
├── api/
│   └── cmd/server/          (point d’entrée HTTP)
├── docs/                    (swagger.json, swag docs…)
├── internal/
│   ├── auth/
│   ├── config/
│   ├── db/
│   ├── http/
│   ├── middleware/
│   ├── models/
│   └── realtime/            (config, types, serveur temps réel)
├── media/
│   └── rtmp/                (stack MediaMTX)
├── pkg/
│   └── ...                  (packages partagés)
├── go.mod / go.sum
├── README.md
└── ...


```

### Commandes utiles

```bash
# Formatage du code
gofmt -w internal/ pkg/ api/

# Génération de la documentation Swagger
swag init -g api/cmd/server/main.go -o docs

# Tests
go test ./...

# Build avec cache personnalisé
GOCACHE=$(pwd)/.gocache go build ./...

# Linting
golangci-lint run
```

### Tests

```bash
# Tests unitaires
go test ./internal/...

# Tests d'intégration
go test ./tests/integration/...

# Tests avec couverture
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

---

## 📚 Documentation

- [Architecture](docs/architecture.md)
- [API Reference](docs/api.md)
- [Streaming Guide](media/rtmp/docs/overview.md)
- [Configuration avancée](docs/configuration.md)

---

## 🤝 Contribution

1. Fork le projet
2. Créez votre branche (`git checkout -b feature/amazing-feature`)
3. Commitez vos changements (`git commit -m 'Add amazing feature'`)
4. Push vers la branche (`git push origin feature/amazing-feature`)
5. Ouvrez une Pull Request

---

## 📄 Licence

Ce projet est sous licence MIT. Voir le fichier [LICENSE](LICENSE) pour plus de détails.

---

## 🆘 Support

- **Issues** : [GitHub Issues](https://github.com/your-org/golive/issues)
- **Documentation** : [Wiki](https://github.com/your-org/golive/wiki)
- **Discord** : [Serveur de support](https://discord.gg/your-server)

---

*GoLive - Une plateforme moderne pour le chat temps réel et le streaming* 🎥💬
