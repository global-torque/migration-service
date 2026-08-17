--- required_env: !master
CREATE TABLE user_users (
    id serial not null primary key,
    name varchar(150) not null default ''
);