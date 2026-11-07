-- Fixture schema B for pgschema --self-test diff checks: exactly the
-- schema.sql tables plus one added column (book.isbn).
CREATE TABLE author (
    id bigserial PRIMARY KEY,
    name text NOT NULL,
    email text NOT NULL UNIQUE
);

CREATE TABLE book (
    id bigserial PRIMARY KEY,
    author_id bigint NOT NULL REFERENCES author(id),
    title text NOT NULL,
    published_year integer,
    isbn character varying(20)
);

CREATE INDEX book_title_idx ON book (title);

INSERT INTO author (id, name, email) VALUES
    (1, 'Ada Lovelace', 'ada@example.com'),
    (2, 'Grace Hopper', 'grace@example.com');

INSERT INTO book (id, author_id, title, published_year, isbn) VALUES
    (1, 1, 'Notes on the Engine', 1843, '978-0-00-000000-1'),
    (2, 2, 'COBOL', 1959, '978-0-00-000000-2');
