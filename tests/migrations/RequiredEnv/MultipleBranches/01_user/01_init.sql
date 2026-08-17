--- required_env: stage|dev|uat
CREATE TABLE user_users (
    id serial not null primary key,
    name varchar(150) not null default ''
);