-- Fixture schema for pgschema --self-test and end-to-end checks.
-- Two tables, a primary key on each, a foreign key, a unique index and a
-- plain index, plus a few rows of sample data. Neutral names only.
CREATE TABLE author (
    id bigserial PRIMARY KEY,
    name text NOT NULL,
    email text NOT NULL UNIQUE
);

CREATE TABLE book (
    id bigserial PRIMARY KEY,
    author_id bigint NOT NULL REFERENCES author(id),
    title text NOT NULL,
    published_year integer
);

CREATE INDEX book_title_idx ON book (title);

INSERT INTO author (id, name, email) VALUES
    (1, 'Ada Lovelace', 'ada@example.com'),
    (2, 'Grace Hopper', 'grace@example.com');

INSERT INTO book (id, author_id, title, published_year) VALUES
    (1, 1, 'Notes on the Engine', 1843),
    (2, 2, 'COBOL', 1959);
