-- A run is one execution of an agent on one input.
create table runs (
    id                 text primary key,
    agent_id           text not null references agents (id) on delete cascade,
    status             text not null
                       check (status in ('running', 'awaiting_approval', 'succeeded', 'failed')),
    -- What started the run: manual, webhook, schedule, or event:<name>.
    trigger            text not null,
    input              jsonb,
    -- Ground truth for grading. Never shown to the agent.
    expected           jsonb,
    output             jsonb,
    error              text not null default '',
    provider           text not null,
    model              text not null,
    -- Set on shadow runs: the primary run whose input this one replays.
    shadow_of          text references runs (id) on delete cascade,
    -- Conversation state of a suspended run.
    state              jsonb,
    input_tokens       bigint not null default 0,
    output_tokens      bigint not null default 0,
    cache_read_tokens  bigint not null default 0,
    cache_write_tokens bigint not null default 0,
    cost_usd           double precision not null default 0,
    llm_calls          integer not null default 0,
    tool_calls         integer not null default 0,
    -- Time spent executing, excluding time waiting for approval.
    active_ms          bigint not null default 0,
    started_at         timestamptz not null default now(),
    finished_at        timestamptz,
    graded_at          timestamptz
);

create index runs_agent_idx on runs (agent_id, started_at desc);
create index runs_review_idx on runs (agent_id, finished_at desc) where finished_at is not null;
create index runs_shadow_idx on runs (shadow_of) where shadow_of is not null;

-- The trace of a run: every model call and tool call, in order.
create table steps (
    run_id             text not null references runs (id) on delete cascade,
    seq                integer not null,
    kind               text not null check (kind in ('llm', 'tool')),
    name               text not null,
    input              jsonb,
    output             jsonb,
    is_error           boolean not null default false,
    simulated          boolean not null default false,
    input_tokens       bigint not null default 0,
    output_tokens      bigint not null default 0,
    cache_read_tokens  bigint not null default 0,
    cache_write_tokens bigint not null default 0,
    cost_usd           double precision not null default 0,
    latency_ms         bigint not null default 0,
    created_at         timestamptz not null default now(),
    primary key (run_id, seq)
);

-- A tool call waiting for, or decided by, a person.
create table approvals (
    id          text primary key,
    run_id      text not null references runs (id) on delete cascade,
    agent_id    text not null references agents (id) on delete cascade,
    tool_use_id text not null,
    tool        text not null,
    input       jsonb,
    status      text not null default 'pending'
                check (status in ('pending', 'approved', 'rejected')),
    decided_by  text not null default '',
    reason      text not null default '',
    created_at  timestamptz not null default now(),
    decided_at  timestamptz,
    unique (run_id, tool_use_id)
);

create index approvals_pending_idx on approvals (created_at) where status = 'pending';
