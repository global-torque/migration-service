CREATE or REPLACE function user_status_trigger()
    RETURNS trigger AS $$
DECLARE
    notification json;
    BEGIN
        IF NEW.status <> OLD.status AND NEW.status = 'legal-closed' THEN
            INSERT INTO queue_events (topic, sub_topic, payload) VALUES('offer', NEW.status, to_jsonb(NEW));
        END IF;
        RETURN NEW;
    END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS user_users_status ON user_users;
CREATE TRIGGER user_users_status
    AFTER UPDATE
    ON user_users
    FOR EACH ROW
    WHEN (NEW.status <> OLD.status)
EXECUTE PROCEDURE user_status_trigger();
