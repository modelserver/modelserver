UPDATE plans SET description = 'Same usage limits as Claude Pro' WHERE slug = 'pro' AND description = '';
UPDATE plans SET description = '2x usage limits of Claude Pro' WHERE slug = 'max_2x' AND description = '';
UPDATE plans SET description = 'Same usage limits as Claude Max (5x)' WHERE slug = 'max_5x' AND description = '';
UPDATE plans SET description = 'Same usage limits as Claude Max (20x)' WHERE slug = 'max_20x' AND description = '';
