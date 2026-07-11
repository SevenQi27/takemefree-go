-- activities 表：从 project_test_vps/db/schema.ts (drizzle) 手工翻译。
-- 列名/类型/默认值/索引与 Node 版保持一一对应，作为 Go 迁移的对照基线。
CREATE EXTENSION IF NOT EXISTS pgcrypto; -- gen_random_uuid()

CREATE TABLE IF NOT EXISTS activities (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  title text NOT NULL,
  city text NOT NULL,
  district text,
  address text,
  latitude double precision,
  longitude double precision,
  category text NOT NULL,
  tags text[] NOT NULL DEFAULT '{}',
  start_time timestamptz,
  end_time timestamptz,
  cover_image text,
  free_type text,
  reservation_required boolean NOT NULL DEFAULT false,
  status text NOT NULL DEFAULT 'published',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- 首页主查询走 (status, city, category)；排序用 start_time。
CREATE INDEX IF NOT EXISTS activities_city_status_idx ON activities (city, status);
CREATE INDEX IF NOT EXISTS activities_category_idx ON activities (category);
CREATE INDEX IF NOT EXISTS activities_start_time_idx ON activities (start_time);
