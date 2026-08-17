CREATE TABLE migration_variables (
    id serial PRIMARY KEY,
    secret_value text NOT NULL,
    dynamic_number integer NOT NULL,
    empty_value text NOT NULL
);

INSERT INTO migration_variables (secret_value, dynamic_number, empty_value)
VALUES (
    ${MIGRATION_TEST_SECRET_VALUE},
    ${MIGRATION_TEST_DYNAMIC_NUMBER},
    ${MIGRATION_TEST_EMPTY_VALUE}
);
