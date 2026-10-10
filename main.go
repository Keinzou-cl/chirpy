package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Keinzou-cl/chirpy/internal/auth"
	"github.com/Keinzou-cl/chirpy/internal/database"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

type apiConfig struct {
	fileserverHits atomic.Int32
	database       *database.Queries
	platform       string
	jwtSecret      string
	polkaKey       string
}

type User struct {
	ID          uuid.UUID `json:"id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Email       string    `json:"email"`
	IsChirpyRed bool      `json:"is_chirpy_red"`
}

type Chirp struct {
	ID        uuid.UUID `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Body      string    `json:"body"`
	UserID    uuid.UUID `json:"user_id"`
}

type loginResponse struct {
	User
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
}

func (cfg *apiConfig) middlewareMetricsInc(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg.fileserverHits.Add(1)
		next.ServeHTTP(w, r)
	})
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func (cfg *apiConfig) newRequestsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(fmt.Sprintf(`<html>
  <body>
    <h1>Welcome, Chirpy Admin</h1>
    <p>Chirpy has been visited %d times!</p>
  </body>
</html>
`, cfg.fileserverHits.Load())))
}

func (cfg *apiConfig) resetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondWithError(w, r, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	if cfg.platform != "dev" {
		respondWithError(w, r, http.StatusForbidden, "Reset is only allowed in dev environment")
		return
	}

	cfg.fileserverHits.Store(0)

	err := cfg.database.DeleteUsers(r.Context())
	if err != nil {
		respondWithError(w, r, http.StatusInternalServerError, "Failed to reset users")
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Reset successful"))
}

func profanityFilter(chirp string) string {
	profanities := map[string]bool{"kerfuffle": true, "sharbert": true, "fornax": true}

	words := strings.Split(chirp, " ")

	for i, word := range words {
		if profanities[strings.ToLower(word)] {
			words[i] = "****"
		}
	}
	return strings.Join(words, " ")
}

func respondWithError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func respondWithJSON(w http.ResponseWriter, r *http.Request, code int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(payload)
}

func (cfg *apiConfig) createUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondWithError(w, r, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		respondWithError(w, r, http.StatusBadRequest, "Invalid request payload")
		return
	}

	if req.Email == "" {
		respondWithError(w, r, http.StatusBadRequest, "Email is required")
		return
	}

	if req.Password == "" {
		respondWithError(w, r, http.StatusBadRequest, "Password is required")
		return
	}

	hashedPassword, err := auth.HashPassword(req.Password)
	if err != nil {
		respondWithError(w, r, http.StatusInternalServerError, "Failed to hash password")
		return
	}

	now := time.Now()

	dbUser, err := cfg.database.CreateUser(r.Context(), database.CreateUserParams{
		ID:             uuid.New(),
		Email:          req.Email,
		CreatedAt:      now,
		UpdatedAt:      now,
		HashedPassword: hashedPassword,
	})
	if err != nil {
		respondWithError(w, r, http.StatusInternalServerError, "Failed to create user")
		return
	}

	respondWithJSON(w, r, http.StatusCreated, User{
		ID:          dbUser.ID,
		CreatedAt:   dbUser.CreatedAt,
		UpdatedAt:   dbUser.UpdatedAt,
		Email:       dbUser.Email,
		IsChirpyRed: dbUser.IsChirpyRed,
	})

}

func (cfg *apiConfig) createChirp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondWithError(w, r, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req struct {
		Body string `json:"body"`
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, r, http.StatusUnauthorized, "Missing or invalid token")
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, r, http.StatusUnauthorized, "Invalid token")
		return
	}

	err = json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		respondWithError(w, r, http.StatusBadRequest, "Invalid request payload")
		return
	}

	if req.Body == "" {
		respondWithError(w, r, http.StatusBadRequest, "Chirp body is required")
		return
	}

	if len(req.Body) > 140 {
		respondWithError(w, r, http.StatusBadRequest, "Chirp is too long")
		return
	}

	cleanedBody := profanityFilter(req.Body)
	now := time.Now()

	dbChirp, err := cfg.database.CreateChirp(r.Context(), database.CreateChirpParams{
		ID:        uuid.New(),
		CreatedAt: now,
		UpdatedAt: now,
		Body:      cleanedBody,
		UserID:    userID,
	})
	if err != nil {
		respondWithError(w, r, http.StatusInternalServerError, "Failed to create chirp")
		return
	}

	respondWithJSON(w, r, http.StatusCreated, Chirp{
		ID:        dbChirp.ID,
		CreatedAt: dbChirp.CreatedAt,
		UpdatedAt: dbChirp.UpdatedAt,
		Body:      dbChirp.Body,
		UserID:    dbChirp.UserID,
	})
}

func (cfg *apiConfig) getChirps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondWithError(w, r, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	authorID := r.URL.Query().Get("author_id")
	sortOrder := r.URL.Query().Get("sort")

	var chirps []database.Chirp
	var err error

	if authorID != "" {
		authorUUID, err := uuid.Parse(authorID)
		if err != nil {
			respondWithError(w, r, http.StatusBadRequest, "Invalid Author ID")
			return
		}

		chirps, err = cfg.database.GetChirpsByAuthor(r.Context(), authorUUID)
		if err != nil {
			respondWithError(w, r, http.StatusInternalServerError, "Failed to retrieve chirps")
			return
		}
	} else {
		chirps, err = cfg.database.RetrieveChirps(r.Context())
		if err != nil {
			respondWithError(w, r, http.StatusInternalServerError, "Failed to retrieve chirps")
			return
		}
	}

	if sortOrder == "desc" {
		sort.Slice(chirps, func(i, j int) bool {
			return chirps[i].CreatedAt.After(chirps[j].CreatedAt)
		})
	} else {
		sort.Slice(chirps, func(i, j int) bool {
			return chirps[i].CreatedAt.Before(chirps[j].CreatedAt)
		})
	}

	response := make([]Chirp, 0, len(chirps))
	for _, dbChirp := range chirps {
		response = append(response, Chirp{
			ID:        dbChirp.ID,
			CreatedAt: dbChirp.CreatedAt,
			UpdatedAt: dbChirp.UpdatedAt,
			Body:      dbChirp.Body,
			UserID:    dbChirp.UserID,
		})
	}

	respondWithJSON(w, r, http.StatusOK, response)
}

func (cfg *apiConfig) getSingleChirp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		respondWithError(w, r, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	chirpIDStr := r.PathValue("chirpID")
	chirpID, err := uuid.Parse(chirpIDStr)
	if err != nil {
		respondWithError(w, r, http.StatusBadRequest, "Invalid chirp ID")
		return
	}

	dbChirp, err := cfg.database.RetrieveChirp(r.Context(), chirpID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			respondWithError(w, r, http.StatusNotFound, "Chirp not found")
			return
		}
		respondWithError(w, r, http.StatusInternalServerError, "Failed to retrieve chirp")
		return
	}

	respondWithJSON(w, r, http.StatusOK, Chirp{
		ID:        dbChirp.ID,
		CreatedAt: dbChirp.CreatedAt,
		UpdatedAt: dbChirp.UpdatedAt,
		Body:      dbChirp.Body,
		UserID:    dbChirp.UserID,
	})
}

func (cfg *apiConfig) loginUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondWithError(w, r, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		respondWithError(w, r, http.StatusBadRequest, "Invalid request payload")
		return
	}

	if req.Email == "" || req.Password == "" {
		respondWithError(w, r, http.StatusBadRequest, "Incorrect email or password")
		return
	}

	dbUser, err := cfg.database.GetUserbyEmail(r.Context(), req.Email)
	if err != nil {
		respondWithError(w, r, http.StatusUnauthorized, "Incorrect email or password")
		return
	}

	match, err := auth.CheckPasswordHash(req.Password, dbUser.HashedPassword)
	if err != nil || !match {
		respondWithError(w, r, http.StatusUnauthorized, "Incorrect email or password")
		return
	}

	now := time.Now().UTC()
	expiresIn := time.Hour

	token, err := auth.MakeJWT(dbUser.ID, cfg.jwtSecret, expiresIn)
	if err != nil {
		respondWithError(w, r, http.StatusInternalServerError, "Failed to create token")
		return
	}

	refreshToken := auth.MakeRefreshToken()
	_, err = cfg.database.CreateRefreshToken(r.Context(), database.CreateRefreshTokenParams{
		Token:     refreshToken,
		CreatedAt: now,
		UpdatedAt: now,
		UserID:    dbUser.ID,
		ExpiresAt: time.Now().UTC().Add(60 * 24 * time.Hour),
	})
	if err != nil {
		respondWithError(w, r, http.StatusInternalServerError, "Failed to create refresh token")
		return
	}

	respondWithJSON(w, r, http.StatusOK, loginResponse{
		User: User{
			ID:          dbUser.ID,
			CreatedAt:   dbUser.CreatedAt,
			UpdatedAt:   dbUser.UpdatedAt,
			Email:       dbUser.Email,
			IsChirpyRed: dbUser.IsChirpyRed,
		},
		Token:        token,
		RefreshToken: refreshToken,
	})
}

func (cfg *apiConfig) refreshToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondWithError(w, r, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	refreshToken, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, r, http.StatusUnauthorized, "Missing or invalid token")
		return
	}

	dbToken, err := cfg.database.GetUserFromRefreshToken(r.Context(), refreshToken)
	if err != nil {
		respondWithError(w, r, http.StatusUnauthorized, "Invalid refresh token")
		return
	}

	if dbToken.ExpiresAt.Before(time.Now().UTC()) {
		respondWithError(w, r, http.StatusUnauthorized, "Refresh token expired")
		return
	}

	newToken, err := auth.MakeJWT(dbToken.UserID, cfg.jwtSecret, time.Hour)
	if err != nil {
		respondWithError(w, r, http.StatusInternalServerError, "Failed to create new token")
		return
	}

	respondWithJSON(w, r, http.StatusOK, map[string]string{"token": newToken})

}

func (cfg *apiConfig) revokeRefreshToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondWithError(w, r, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	refreshToken, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, r, http.StatusUnauthorized, "Missing or invalid token")
		return
	}

	err = cfg.database.RevokeRefreshToken(r.Context(), refreshToken)
	if err != nil {
		respondWithError(w, r, http.StatusInternalServerError, "Failed to revoke refresh token")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (cfg *apiConfig) updateUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		respondWithError(w, r, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		respondWithError(w, r, http.StatusBadRequest, "Invalid request payload")
		return
	}

	if req.Email == "" || req.Password == "" {
		respondWithError(w, r, http.StatusBadRequest, "Email and password are required")
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, r, http.StatusUnauthorized, "Missing or invalid token")
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, r, http.StatusUnauthorized, "Invalid token")
		return
	}

	hashedPassword, err := auth.HashPassword(req.Password)
	if err != nil {
		respondWithError(w, r, http.StatusInternalServerError, "Failed to hash password")
		return
	}

	now := time.Now().UTC()

	updatedUser, err := cfg.database.UpdateUser(r.Context(), database.UpdateUserParams{
		ID:             userID,
		Email:          req.Email,
		HashedPassword: hashedPassword,
		UpdatedAt:      now,
	})
	if err != nil {
		respondWithError(w, r, http.StatusInternalServerError, "Failed to update user")
		return
	}

	respondWithJSON(w, r, http.StatusOK, User{
		ID:        updatedUser.ID,
		CreatedAt: updatedUser.CreatedAt,
		UpdatedAt: updatedUser.UpdatedAt,
		Email:     updatedUser.Email,
	})
}

func (cfg *apiConfig) deleteSingleChirp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		respondWithError(w, r, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	chirpIDStr := r.PathValue("chirpID")
	chirpID, err := uuid.Parse(chirpIDStr)
	if err != nil {
		respondWithError(w, r, http.StatusBadRequest, "Invalid chirp ID")
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, r, http.StatusUnauthorized, "Missing or invalid token")
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, r, http.StatusUnauthorized, "Invalid token")
		return
	}

	dbChirp, err := cfg.database.RetrieveChirp(r.Context(), chirpID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			respondWithError(w, r, http.StatusNotFound, "Chirp not found")
			return
		}
		respondWithError(w, r, http.StatusInternalServerError, "Failed to retrieve chirp")
		return
	}

	if dbChirp.UserID != userID {
		respondWithError(w, r, http.StatusForbidden, "You are not allowed to delete this chirp")
		return
	}

	err = cfg.database.DeleteSingleChirp(r.Context(), chirpID)
	if err != nil {
		respondWithError(w, r, http.StatusInternalServerError, "Failed to delete chirp")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (cfg *apiConfig) webhooks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondWithError(w, r, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	var req struct {
		Event string `json:"event"`
		Data  struct {
			UserID string `json:"user_id"`
		} `json:"data"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondWithError(w, r, http.StatusBadRequest, "Invalid request body")
		return
	}

	apiKey, err := auth.GetAPIKey(r.Header)
	if err != nil {
		respondWithError(w, r, http.StatusUnauthorized, "Unauthorized")
		return
	}

	if apiKey != cfg.polkaKey {
		respondWithError(w, r, http.StatusUnauthorized, "Unauthorized")
		return
	}

	switch req.Event {
	case "user.upgraded":
		userID, err := uuid.Parse(req.Data.UserID)
		if err != nil {
			respondWithError(w, r, http.StatusBadRequest, "Invalid user ID")
			return
		}
		_, err = cfg.database.UpgradeUserToChirpyRed(r.Context(), userID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				respondWithError(w, r, http.StatusNotFound, "User not found")
				return
			}

			respondWithError(w, r, http.StatusInternalServerError, "Failed to upgrade user")
			return
		}
	default:
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, using existing environment")
	}
	dbURL := os.Getenv("DB_URL")
	platform := os.Getenv("PLATFORM")
	jwtSecret := os.Getenv("JWT_SECRET")
	polkaKey := os.Getenv("POLKA_KEY")

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		panic(err)
	}

	dbQueries := database.New(db)

	req := http.NewServeMux()
	apiCfg := &apiConfig{
		database:  dbQueries,
		platform:  platform,
		jwtSecret: jwtSecret,
		polkaKey:  polkaKey,
	}

	req.Handle("/app/", http.StripPrefix("/app", apiCfg.middlewareMetricsInc(http.FileServer(http.Dir(".")))))
	req.HandleFunc("GET /api/healthz", healthHandler)
	req.HandleFunc("GET /admin/metrics", apiCfg.newRequestsHandler)
	req.HandleFunc("POST /admin/reset", apiCfg.resetHandler)
	req.HandleFunc("POST /api/chirps", apiCfg.createChirp)
	req.HandleFunc("GET /api/chirps", apiCfg.getChirps)
	req.HandleFunc("GET /api/chirps/{chirpID}", apiCfg.getSingleChirp)
	req.HandleFunc("POST /api/users", apiCfg.createUsers)
	req.HandleFunc("POST /api/login", apiCfg.loginUser)
	req.HandleFunc("POST /api/refresh", apiCfg.refreshToken)
	req.HandleFunc("POST /api/revoke", apiCfg.revokeRefreshToken)
	req.HandleFunc("PUT /api/users", apiCfg.updateUsers)
	req.HandleFunc("DELETE /api/chirps/{chirpID}", apiCfg.deleteSingleChirp)
	req.HandleFunc("POST /api/polka/webhooks", apiCfg.webhooks)
	server := http.Server{
		Addr:    ":8080",
		Handler: req,
	}

	if err := server.ListenAndServe(); err != nil {
		panic(err)
	}

}
