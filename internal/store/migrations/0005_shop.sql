-- The demo store: a small online shop for the example agents to work on.
-- It lives in its own schema and nothing in Roster depends on it.
create schema shop;

create sequence shop.customer_seq start 1000;
create sequence shop.order_seq start 5000;
create sequence shop.ticket_seq start 100;
create sequence shop.product_seq start 100;
create sequence shop.po_seq start 1;

create table shop.customers (
    id           text primary key,
    email        text not null,
    name         text not null,
    country      text not null,
    orders_count integer not null default 0,
    chargebacks  integer not null default 0,
    created_at   timestamptz not null default now()
);

create table shop.products (
    sku         text primary key,
    title       text not null,
    description text not null default '',
    category    text not null default '',
    color       text not null default '',
    material    text not null default '',
    price_cents integer not null,
    created_at  timestamptz not null default now()
);

create table shop.inventory (
    sku             text primary key references shop.products (sku) on delete cascade,
    on_hand         integer not null,
    on_order        integer not null default 0,
    daily_sales     double precision not null,
    lead_time_days  integer not null,
    -- Days of stock the shop wants left when a delivery arrives.
    cover_days      integer not null,
    -- Suppliers sell in multiples of this.
    pack_size       integer not null,
    unit_cost_cents integer not null
);

create table shop.orders (
    id               text primary key,
    customer_id      text not null references shop.customers (id) on delete cascade,
    status           text not null
                     check (status in ('processing', 'shipped', 'delivered', 'cancelled', 'on_hold')),
    items            text not null,
    total_cents      integer not null,
    billing_country  text not null,
    shipping_country text not null,
    ip_country       text not null,
    payment_attempts integer not null default 1,
    carrier          text not null default '',
    tracking         text not null default '',
    eta              text not null default '',
    hold_reason      text not null default '',
    placed_at        timestamptz not null default now()
);

create table shop.tickets (
    id                text primary key,
    customer_id       text not null references shop.customers (id) on delete cascade,
    order_id          text references shop.orders (id) on delete set null,
    subject           text not null,
    body              text not null,
    status            text not null default 'open'
                      check (status in ('open', 'answered', 'escalated')),
    reply             text not null default '',
    escalation_reason text not null default '',
    created_at        timestamptz not null default now()
);

create table shop.purchase_orders (
    id         text primary key,
    sku        text not null references shop.products (sku) on delete cascade,
    quantity   integer not null check (quantity > 0),
    note       text not null default '',
    created_at timestamptz not null default now()
);
