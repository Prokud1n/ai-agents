-- Brand knowledge base: documents split into chunks with embeddings, and the trainer's corrections
-- to agents' drafts. Vectors are 1536-dimensional; search is by cosine distance and only among
-- vectors of the current embedding model (vectors of different models are not comparable).

CREATE TABLE kb_documents (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source          text        NOT NULL UNIQUE, -- path relative to the folder given to `kb ingest`
    title           text        NOT NULL,
    kind            text        NOT NULL CHECK (kind IN ('brand', 'product', 'post', 'other')),
    checksum        text        NOT NULL, -- sha256 of the extracted text
    embedding_model text        NOT NULL, -- model of the chunks' embeddings
    chunker_version integer     NOT NULL, -- chunk.Version used to split the text
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX kb_documents_kind_idx ON kb_documents (kind);

CREATE TABLE kb_chunks (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    document_id bigint       NOT NULL REFERENCES kb_documents (id) ON DELETE CASCADE,
    ord         integer      NOT NULL, -- position of the chunk in the document
    content     text         NOT NULL, -- section heading path + text
    embedding   vector(1536) NOT NULL,
    tokens      integer      NOT NULL, -- approximate token count
    UNIQUE (document_id, ord)
);

-- Filtered searches (by kind, by model) use hnsw.iterative_scan (pgvector >= 0.8).
CREATE INDEX kb_chunks_embedding_idx ON kb_chunks USING hnsw (embedding vector_cosine_ops);

CREATE TABLE kb_corrections (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent           text        NOT NULL,
    task_kind       text        NOT NULL, -- e.g. reels-script
    original        text        NOT NULL, -- agent's draft
    corrected       text        NOT NULL, -- trainer's version
    comment         text        NOT NULL DEFAULT '',
    -- Embedding of the comment and the original; NULL when embeddings were unavailable on save
    -- (`kb reembed` fills it in).
    embedding       vector(1536),
    embedding_model text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- Corrections are searched exactly within one agent and task kind: few rows, no ANN index needed.
CREATE INDEX kb_corrections_agent_task_kind_idx ON kb_corrections (agent, task_kind, created_at DESC);
