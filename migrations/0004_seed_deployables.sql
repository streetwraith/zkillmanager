-- 0004_seed_deployables: classify every published Deployable (SDE category 22) as
-- 'Deployables' in victim_bucket, so deployable kills stop falling into the reader's
-- catch-all 'Other' target bucket. Sourced by category rather than by the groups seen so
-- far, so a deployable nobody has killed yet is still covered. Fighters (category 87),
-- drones (18), and structures (65) are deliberately left unclassified (owner's decision).
--
-- victim_bucket is joined at read time, so this reclassifies existing deployable kills with
-- no data rewrite. Category 22 is disjoint from the 0002 seed (category 6 ships), so nothing
-- here conflicts; ON CONFLICT DO NOTHING is belt-and-suspenders.
INSERT INTO killmails.victim_bucket (type_id, bucket)
SELECT t._key, 'Deployables'
FROM sde.types t
JOIN sde.groups g ON g._key = t.group_id
WHERE g.category_id = 22 AND t.published
ON CONFLICT (type_id) DO NOTHING;
