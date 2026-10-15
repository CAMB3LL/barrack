INSERT INTO public.foo (created_at, updated_at)
SELECT CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
FROM generate_series(1, 5);
