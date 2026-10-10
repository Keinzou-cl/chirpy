-- name: CreateRefreshToken :one
INSERT INTO refresh_tokens (token, created_at, updated_at, user_id, expires_at)
VALUES (
    $1,
    $2,
    $3,
    $4,
    $5
)
RETURNING *;

-- name: GetUserFromRefreshToken :one
SELECT
    *
FROM
    refresh_tokens
WHERE
    token = $1
    AND revoked_at IS NULL
    AND expires_at > NOW();

-- name: RevokeRefreshToken :exec
UPDATE refresh_tokens
SET revoked_at = NOW(),
    updated_at = NOW()
WHERE
    token = $1;

-- name: UpdateUser :one
UPDATE users
SET email = $1,
    hashed_password = $2,
    updated_at = $3
WHERE id = $4
RETURNING id, created_at, updated_at, email;

-- name: DeleteSingleChirp :exec
DELETE FROM chirps
WHERE id = $1;

-- name: UpgradeUserToChirpyRed :one
UPDATE users
SET is_chirpy_red = TRUE,
    updated_at = NOW()
WHERE id = $1
RETURNING id, created_at, updated_at, email, is_chirpy_red;

-- name: GetChirpsByAuthor :many
SELECT *
FROM chirps
WHERE user_id = $1
ORDER BY created_at DESC;