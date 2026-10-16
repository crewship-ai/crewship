-- Rebuildable native content cache. It is neither Page authority nor backup content.
CREATE TABLE page_search_writer_fence (
 id INTEGER PRIMARY KEY CHECK(id=1),
 seq INTEGER NOT NULL
);
INSERT INTO page_search_writer_fence(id,seq) VALUES(1,0);

CREATE TABLE page_search_projections (
 workspace_id TEXT NOT NULL,
 page_id TEXT NOT NULL,
 adapter_version INTEGER NOT NULL CHECK(adapter_version>=1),
 source_type TEXT NOT NULL CHECK(source_type IN ('document','slides')),
 source_schema INTEGER NOT NULL CHECK(source_schema>=1),
 source_rev INTEGER NOT NULL CHECK(source_rev>=0 AND (source_type<>'slides' OR source_rev>=1)),
 source_generation INTEGER NOT NULL CHECK(source_generation>=0 AND (source_type<>'slides' OR source_generation=0)),
 source_hash TEXT NOT NULL CHECK(length(source_hash)=64 AND source_hash NOT GLOB '*[^0-9a-f]*'),
 encoded BLOB NOT NULL CHECK(typeof(encoded)='blob' AND length(encoded)>0 AND length(encoded)<=4194304),
 encoded_sha256 TEXT NOT NULL CHECK(length(encoded_sha256)=64 AND encoded_sha256 NOT GLOB '*[^0-9a-f]*'),
 PRIMARY KEY(workspace_id,page_id),
 FOREIGN KEY(workspace_id,page_id) REFERENCES pages(workspace_id,id) ON DELETE CASCADE
);
CREATE INDEX idx_page_search_projections_page ON page_search_projections(page_id,workspace_id);
