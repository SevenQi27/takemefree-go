-- 种子数据：对照 db/seed.ts 的样本形状（相对时间，保证既有未过期也有已过期样本）。
INSERT INTO activities
  (title, city, district, address, latitude, longitude, category, tags, start_time, end_time, free_type, reservation_required, status)
VALUES
  ('外滩美术馆免费开放日', 'shanghai', '黄浦区', '虎丘路 20 号', 31.2406, 121.4903, 'exhibition',
   ARRAY['室内','展览','免预约'], now() + interval '1 day', now() + interval '1 day', '完全免费', false, 'published'),
  ('徐汇滨江公共艺术市集', 'shanghai', '徐汇区', '龙腾大道', 31.181, 121.461, 'event',
   ARRAY['户外','亲子友好'], now() + interval '3 day', now() + interval '4 day', '完全免费', false, 'published'),
  ('城市图书馆夜读沙龙（需预约）', 'shanghai', '浦东新区', '迎春路 300 号', NULL, NULL, 'welfare',
   ARRAY['室内','讲座'], now() + interval '5 day', now() + interval '5 day', '限时免费', true, 'published'),
  ('已过期的展览（不应出现在接口结果里）', 'shanghai', '静安区', '南京西路 100 号', NULL, NULL, 'exhibition',
   ARRAY['室内'], now() - interval '10 day', now() - interval '3 day', '完全免费', false, 'published'),
  ('长期开放的公共空间（end_time 为空）', 'shanghai', '杨浦区', '杨树浦路 555 号', NULL, NULL, 'public-space',
   ARRAY['户外','长期'], NULL, NULL, '完全免费', false, 'published'),
  ('草稿状态活动（不应出现在接口结果里）', 'shanghai', '虹口区', '四川北路 1 号', NULL, NULL, 'event',
   ARRAY['室内'], now() + interval '2 day', now() + interval '2 day', '完全免费', false, 'draft'),
  ('798 艺术区免费导览', 'beijing', '朝阳区', '酒仙桥路 4 号', 39.984, 116.497, 'exhibition',
   ARRAY['户外','导览'], now() + interval '2 day', now() + interval '2 day', '完全免费', false, 'published'),
  ('深圳湾公园晨跑团', 'shenzhen', '南山区', '滨海大道', 22.516, 113.947, 'event',
   ARRAY['户外','运动'], now() + interval '1 day', NULL, '完全免费', false, 'published');
