-- api/internal/models/models.sql

-- Enable extensions
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "citext";

-- Users table with enhanced fields
CREATE TABLE IF NOT EXISTS users (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    email           CITEXT UNIQUE NOT NULL,
    username        VARCHAR(50) UNIQUE NOT NULL,
    password_hash   TEXT NOT NULL,
    display_name    VARCHAR(100),
    avatar_url      TEXT,
    bio            TEXT,
    is_active       BOOLEAN DEFAULT true,
    is_verified     BOOLEAN DEFAULT false,
    role           VARCHAR(20) DEFAULT 'user' CHECK (role IN ('user', 'streamer', 'moderator', 'admin')),
    
    -- Profile settings
    settings        JSONB DEFAULT '{}',
    
    -- Timestamps
    email_verified_at TIMESTAMPTZ,
    last_login_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Streams table with enhanced metadata
CREATE TABLE IF NOT EXISTS streams (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    
    -- Stream identification
    key            VARCHAR(100) UNIQUE NOT NULL,
    title          VARCHAR(200) NOT NULL,
    description    TEXT,
    category       VARCHAR(50),
    language       VARCHAR(10) DEFAULT 'en',
    
    -- Stream settings
    is_live        BOOLEAN NOT NULL DEFAULT false,
    is_public      BOOLEAN NOT NULL DEFAULT true,
    is_mature      BOOLEAN NOT NULL DEFAULT false,
    max_viewers    INTEGER,
    chat_enabled   BOOLEAN NOT NULL DEFAULT true,
    
    -- Stream metadata
    thumbnail_url  TEXT,
    preview_url    TEXT,
    tags          TEXT[],
    
    -- Analytics
    current_viewers INTEGER DEFAULT 0,
    peak_viewers   INTEGER DEFAULT 0,
    total_views    BIGINT DEFAULT 0,
    follower_count BIGINT DEFAULT 0,
    
    -- Timestamps
    started_at     TIMESTAMPTZ,
    ended_at       TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Stream sessions for analytics
CREATE TABLE IF NOT EXISTS stream_sessions (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    stream_id       UUID NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
    
    -- Session data
    started_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at        TIMESTAMPTZ,
    duration_seconds INTEGER,
    peak_viewers    INTEGER DEFAULT 0,
    avg_viewers     INTEGER DEFAULT 0,
    total_messages  INTEGER DEFAULT 0,
    
    -- Quality metrics
    bitrate_avg     INTEGER,
    bitrate_peak    INTEGER,
    dropped_frames  INTEGER DEFAULT 0,
    
    -- Metadata
    metadata        JSONB DEFAULT '{}'
);

-- Chat messages with enhanced features
CREATE TABLE IF NOT EXISTS chat_messages (
    id              BIGSERIAL PRIMARY KEY,
    stream_id       UUID NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
    user_id         UUID REFERENCES users(id) ON DELETE SET NULL,
    
    -- Message data
    content         TEXT NOT NULL,
    message_type    VARCHAR(20) DEFAULT 'message' CHECK (message_type IN ('message', 'system', 'emote', 'command')),
    
    -- User info (for anonymous or deleted users)
    username        VARCHAR(50),
    display_name    VARCHAR(100),
    user_color      VARCHAR(7), -- hex color
    
    -- Message metadata
    is_deleted      BOOLEAN DEFAULT false,
    is_pinned       BOOLEAN DEFAULT false,
    reply_to_id     BIGINT REFERENCES chat_messages(id),
    
    -- Moderation
    moderated_by    UUID REFERENCES users(id),
    moderated_at    TIMESTAMPTZ,
    moderation_reason TEXT,
    
    -- Analytics
    emotes          TEXT[],
    mentions        TEXT[],
    
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Followers system
CREATE TABLE IF NOT EXISTS follows (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    follower_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    following_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    
    -- Settings
    notifications   BOOLEAN DEFAULT true,
    
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    
    UNIQUE(follower_id, following_id),
    CHECK(follower_id != following_id)
);

-- Stream categories
CREATE TABLE IF NOT EXISTS categories (
    id              SERIAL PRIMARY KEY,
    name            VARCHAR(100) UNIQUE NOT NULL,
    slug            VARCHAR(100) UNIQUE NOT NULL,
    description     TEXT,
    thumbnail_url   TEXT,
    is_active       BOOLEAN DEFAULT true,
    sort_order      INTEGER DEFAULT 0,
    
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- User sessions for auth
CREATE TABLE IF NOT EXISTS user_sessions (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash      TEXT NOT NULL,
    
    -- Session metadata
    ip_address      INET,
    user_agent      TEXT,
    device_type     VARCHAR(50),
    
    -- Timestamps
    expires_at      TIMESTAMPTZ NOT NULL,
    last_used_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Moderation logs
CREATE TABLE IF NOT EXISTS moderation_logs (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    stream_id       UUID REFERENCES streams(id) ON DELETE CASCADE,
    moderator_id    UUID NOT NULL REFERENCES users(id),
    target_user_id  UUID REFERENCES users(id) ON DELETE SET NULL,
    
    -- Action details
    action_type     VARCHAR(50) NOT NULL, -- ban, timeout, delete_message, etc.
    reason          TEXT,
    duration        INTERVAL, -- for timeouts
    
    -- References
    message_id      BIGINT REFERENCES chat_messages(id),
    
    -- Metadata
    metadata        JSONB DEFAULT '{}',
    
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Stream analytics (hourly aggregations)
CREATE TABLE IF NOT EXISTS stream_analytics (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    stream_id       UUID NOT NULL REFERENCES streams(id) ON DELETE CASCADE,
    
    -- Time bucket
    hour_bucket     TIMESTAMPTZ NOT NULL,
    
    -- Metrics
    unique_viewers  INTEGER DEFAULT 0,
    peak_viewers    INTEGER DEFAULT 0,
    avg_viewers     INTEGER DEFAULT 0,
    chat_messages   INTEGER DEFAULT 0,
    new_followers   INTEGER DEFAULT 0,
    
    -- Quality metrics
    avg_bitrate     INTEGER,
    dropped_frames  INTEGER DEFAULT 0,
    
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    
    UNIQUE(stream_id, hour_bucket)
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_streams_user_id ON streams(user_id);
CREATE INDEX IF NOT EXISTS idx_streams_is_live ON streams(is_live) WHERE is_live = true;
CREATE INDEX IF NOT EXISTS idx_streams_category ON streams(category);
CREATE INDEX IF NOT EXISTS idx_streams_created_at ON streams(created_at);

CREATE INDEX IF NOT EXISTS idx_chat_messages_stream_id ON chat_messages(stream_id);
CREATE INDEX IF NOT EXISTS idx_chat_messages_user_id ON chat_messages(user_id);
CREATE INDEX IF NOT EXISTS idx_chat_messages_created_at ON chat_messages(created_at);
CREATE INDEX IF NOT EXISTS idx_chat_messages_is_deleted ON chat_messages(is_deleted);

CREATE INDEX IF NOT EXISTS idx_follows_follower_id ON follows(follower_id);
CREATE INDEX IF NOT EXISTS idx_follows_following_id ON follows(following_id);

CREATE INDEX IF NOT EXISTS idx_user_sessions_user_id ON user_sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_user_sessions_expires_at ON user_sessions(expires_at);

CREATE INDEX IF NOT EXISTS idx_stream_analytics_stream_id ON stream_analytics(stream_id);
CREATE INDEX IF NOT EXISTS idx_stream_analytics_hour_bucket ON stream_analytics(hour_bucket);

-- Ensure function exists
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ language 'plpgsql';

-- Drop triggers if they already exist
DROP TRIGGER IF EXISTS update_users_updated_at ON users;
DROP TRIGGER IF EXISTS update_streams_updated_at ON streams;
DROP TRIGGER IF EXISTS update_categories_updated_at ON categories;

-- Recreate triggers
CREATE TRIGGER update_users_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_streams_updated_at
    BEFORE UPDATE ON streams
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

CREATE TRIGGER update_categories_updated_at
    BEFORE UPDATE ON categories
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();


-- Insert default categories
INSERT INTO categories (name, slug, description) VALUES
('Just Chatting', 'just-chatting', 'General conversation and interaction'),
('Gaming', 'gaming', 'Video games and gaming content'),
('Music', 'music', 'Musical performances and content'),
('Art', 'art', 'Creative arts and drawing'),
('Tech & Programming', 'tech-programming', 'Technology and programming content'),
('IRL', 'irl', 'In Real Life content')
ON CONFLICT (slug) DO NOTHING;