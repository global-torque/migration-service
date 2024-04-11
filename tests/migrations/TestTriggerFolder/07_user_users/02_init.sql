CREATE TABLE user_users 
(
  id              SERIAL PRIMARY KEY,

  -- required
  name character      varying(150) NOT NULL,
  slug character      varying(150) NOT NULL UNIQUE,
  status character    varying(150) DEFAULT 'new',

  -- optional
  description         text NOT NULL DEFAULT '',

  -- dates
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now()
);
