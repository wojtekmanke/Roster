-- The value of one KPI for one run. Scores are keyed by KPI name because two
-- KPIs may use the same grader with different parameters.
create table scores (
    run_id     text not null references runs (id) on delete cascade,
    agent_id   text not null references agents (id) on delete cascade,
    kpi        text not null,
    grader     text not null,
    metric     text not null,
    value      double precision not null,
    -- Whatever the grader wants to show a person: mismatched fields, a judge's reasoning.
    detail     jsonb,
    created_at timestamptz not null default now(),
    primary key (run_id, kpi)
);

create index scores_agent_idx on scores (agent_id, kpi);

-- Finished runs still waiting to be graded.
create index runs_ungraded_idx on runs (finished_at) where finished_at is not null and graded_at is null;

-- Single-row table remembering when schedules were last evaluated, so firing
-- times that fall while no instance is running are not lost.
create table scheduler_state (
    id        boolean primary key default true check (id),
    last_tick timestamptz not null
);
