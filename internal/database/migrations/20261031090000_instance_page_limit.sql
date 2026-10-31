-- Instance-wide cap on pages per workspace. One row; the default (100) is the
-- value the cap has always had, so nothing changes until an instance
-- administrator sets it. Lowering it never removes a page: it only refuses new
-- ones while a workspace is at or above the cap.
CREATE TABLE instance_page_limits (
 id INTEGER PRIMARY KEY CHECK(id=1),
 max_pages_per_workspace INTEGER NOT NULL DEFAULT 100 CHECK(max_pages_per_workspace BETWEEN 10 AND 10000)
);
INSERT INTO instance_page_limits(id) VALUES(1);
