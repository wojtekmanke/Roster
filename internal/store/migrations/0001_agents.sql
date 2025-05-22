-- An agent is a named, versioned spec plus its employment status.
create table agents (
    id            text primary key,
    name          text not null unique,
    description   text not null default '',
    spec_yaml     text not null,
    spec_hash     text not null,
    provider      text not null,
    model         text not null,
    status        text not null default 'active'
                  check (status in ('active', 'paused', 'retired')),
    status_reason text not null default '',
    -- Scorecards only count runs finished after this instant. Resuming a
    -- paused agent moves it forward so old failures do not re-trigger a pause.
    review_since  timestamptz not null default now(),
    created_at    timestamptz not null default now(),
    updated_at    timestamptz not null default now()
);

-- Append-only audit trail of everything that changed an agent's status or spec.
create table agent_events (
    id         bigint generated always as identity primary key,
    agent_id   text not null references agents (id) on delete cascade,
    kind       text not null,
    actor      text not null,
    reason     text not null default '',
    data       jsonb,
    created_at timestamptz not null default now()
);

create index agent_events_agent_idx on agent_events (agent_id, created_at desc);
