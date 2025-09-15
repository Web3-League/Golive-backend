CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "citext";

CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    email CITEXT UNIQUE NOT NULL,
    username VARCHAR(50) UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    display_name VARCHAR(100),
    avatar_url TEXT,
    bio TEXT,
    role VARCHAR(20) NOT NULL DEFAULT 'user',
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS streams (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key VARCHAR(100) UNIQUE NOT NULL,
    ingest_key_hash TEXT NOT NULL,
    ingest_key_hint VARCHAR(16),
    title VARCHAR(200) NOT NULL,
    description TEXT,
    category VARCHAR(50),
    language VARCHAR(10) DEFAULT 'en',
    tags TEXT[],
    is_live BOOLEAN NOT NULL DEFAULT false,
    is_public BOOLEAN NOT NULL DEFAULT true,
    is_mature BOOLEAN NOT NULL DEFAULT false,
    chat_enabled BOOLEAN NOT NULL DEFAULT true,
    thumbnail_url TEXT,
    current_viewers INTEGER DEFAULT 0,
    peak_viewers INTEGER DEFAULT 0,
    total_views BIGINT DEFAULT 0,
    follower_count BIGINT DEFAULT 0,
    started_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_streams_user_id ON streams(user_id);
CREATE INDEX IF NOT EXISTS idx_streams_is_live ON streams(is_live) WHERE is_live = true;
CREATE INDEX IF NOT EXISTS idx_streams_category ON streams(category);
CREATE INDEX IF NOT EXISTS idx_streams_created_at ON streams(created_at);
