-- The work queue. Workers claim rows with FOR UPDATE SKIP LOCKED, so the
-- queue needs nothing beyond the database Roster already depends on.
create table jobs (
    id           bigint generated always as identity primary key,
    agent_id     text not null references agents (id) on delete cascade,
    -- run: start a new run. resume: continue a run after its approvals were
    -- decided. shadow: replay a run's input on another model.
    kind         text not null check (kind in ('run', 'resume', 'shadow')),
    -- resume and shadow: the run concerned. run: set once the run row exists.
    run_id       text references runs (id) on delete cascade,
    trigger      text not null default 'manual',
    input        jsonb,
    expected     jsonb,
    -- shadow: which entry of the spec's shadow list to use.
    shadow_index integer,
    -- Enqueueing the same key twice is a no-op. Used by the scheduler.
    dedupe_key   text unique,
    status       text not null default 'queued'
                 check (status in ('queued', 'running', 'done', 'failed')),
    attempts     integer not null default 0,
    run_at       timestamptz not null default now(),
    locked_by    text,
    locked_at    timestamptz,
    last_error   text not null default '',
    created_at   timestamptz not null default now(),
    finished_at  timestamptz
);

create index jobs_ready_idx on jobs (run_at, id) where status = 'queued';
create index jobs_locked_idx on jobs (locked_at) where status = 'running';
