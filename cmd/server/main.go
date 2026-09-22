package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hirotomasato/paygateme/core"
	"github.com/hirotomasato/paygateme/payment"
	"github.com/hirotomasato/paygateme/shopee"
	"github.com/hirotomasato/paygateme/utils"
	"rsc.io/qr"
)

type Server struct {
	svc         *payment.Service
	provider    *shopee.Provider
	session     *shopee.Session
	sessionPath string
	logger      utils.Logger
	port        int
	mu          sync.RWMutex
	callbacks   map[string]string // paymentID -> callbackURL
}

type CreatePaymentRequest struct {
	OrderID          string `json:"order_id"`
	Amount           int64  `json:"amount"`
	ExpiresInMinutes int    `json:"expires_in_minutes,omitempty"`
	CallbackURL      string `json:"callback_url,omitempty"`
}

type CreatePaymentResponse struct {
	Success      bool      `json:"success"`
	PaymentID    string    `json:"payment_id"`
	OrderID      string    `json:"order_id"`
	BaseAmount   int64     `json:"amount"`
	UniqueAmount int64     `json:"unique_amount"`
	UniqueOffset int64     `json:"unique_offset"`
	Status       string    `json:"status"`
	QRISString   string    `json:"qris_string"`
	QRISImageB64 string    `json:"qris_image_base64,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	CreatedAt    time.Time `json:"created_at"`
}

type PaymentStatusResponse struct {
	Success      bool       `json:"success"`
	PaymentID    string     `json:"payment_id"`
	OrderID      string     `json:"order_id"`
	UniqueAmount int64      `json:"unique_amount"`
	Status       string     `json:"status"`
	ExpiresAt    time.Time  `json:"expires_at"`
	PaidAt       *time.Time `json:"paid_at,omitempty"`
	TxID         string     `json:"transaction_id,omitempty"`
	PaymentType  string     `json:"payment_type,omitempty"`
}

func main() {
	port := flag.Int("port", 8080, "server listen port")
	sessionPath := flag.String("session", "session.json", "path to saved session JSON")
	// Masukkan string QRIS statis merchant (format: 00020101...) via flag -qris atau env STATIC_QRIS
	staticQris := flag.String("qris", os.Getenv("STATIC_QRIS"), "static QRIS payload (or set STATIC_QRIS env var)")
	flag.Parse()

	if *staticQris == "" {
		fmt.Fprintln(os.Stderr, "Error: QRIS statis diperlukan. Gunakan flag -qris \"<string_qris>\" atau set env STATIC_QRIS.")
		os.Exit(1)
	}

	data, err := os.ReadFile(*sessionPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s tidak ditemukan: %v\n", *sessionPath, err)
		os.Exit(1)
	}

	var session shopee.Session
	if err := json.Unmarshal(data, &session); err != nil {
		fmt.Fprintf(os.Stderr, "Error membaca JSON %s: %v\n", *sessionPath, err)
		os.Exit(1)
	}

	logger := utils.NewConsoleLogger(utils.LevelInfo)
	allocator := payment.NewExactAmountAllocator(true)

	provider := shopee.NewProvider(shopee.ProviderConfig{
		Session:    &session,
		StaticQris: *staticQris,
		Allocator:  allocator,
		Logger:     logger,
		OnSessionUpdated: func(s shopee.Session) error {
			d, err := json.MarshalIndent(s, "", "  ")
			if err != nil {
				return err
			}
			return os.WriteFile(*sessionPath, d, 0o600)
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := provider.RefreshSession(ctx); err != nil {
		logger.Warn(fmt.Sprintf("Session refresh warning: %v", err), nil)
	}

	svc, err := provider.Payments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Gagal membuat payment service: %v\n", err)
		os.Exit(1)
	}

	srv := &Server{
		svc:         svc,
		provider:    provider,
		session:     &session,
		sessionPath: *sessionPath,
		logger:      logger,
		port:        *port,
		callbacks:   make(map[string]string),
	}

	svc.OnPaid(srv.handlePaymentPaid)
	svc.OnExpired(func(p core.Payment) {
		logger.Info(fmt.Sprintf("Payment %s (Order: %s) expired", p.ID, p.Reference), nil)
	})
	svc.OnError(func(err error) {
		logger.Error(fmt.Sprintf("Feed error: %v", err), nil)
	})

	svc.Start()
	defer svc.Stop()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", srv.handleHealth)
	mux.HandleFunc("POST /api/payments", srv.handleCreatePayment)
	mux.HandleFunc("GET /api/payments/", srv.handleGetPayment)

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", *port),
		Handler:      corsMiddleware(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		logger.Info(fmt.Sprintf("🚀 QRIS Gateway Server berjalan di port :%d", *port), nil)
		logger.Info(fmt.Sprintf("📌 Merchant: %s | Store ID: %s", session.Merchant.Name, session.StoreID), nil)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error(fmt.Sprintf("HTTP server error: %v", err), nil)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	logger.Info("Mematikan server...", nil)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"merchant":  s.session.Merchant.Name,
		"store_id":  s.session.StoreID,
		"timestamp": time.Now().Unix(),
	})
}

func (s *Server) handleCreatePayment(w http.ResponseWriter, r *http.Request) {
	var req CreatePaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "invalid json body"})
		return
	}

	if req.Amount <= 0 {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "amount harus lebih dari 0"})
		return
	}

	if req.OrderID == "" {
		req.OrderID = fmt.Sprintf("ORD-%d", time.Now().UnixNano()%1000000)
	}

	expiryMinutes := 10
	if req.ExpiresInMinutes > 0 && req.ExpiresInMinutes <= 60 {
		expiryMinutes = req.ExpiresInMinutes
	}

	metadata := map[string]any{"order_id": req.OrderID}
	if req.CallbackURL != "" {
		metadata["callback_url"] = req.CallbackURL
	}

	// Batalkan pending payment sebelumnya untuk nominal atau order ID sama agar nominal pas langsung bersih
	if activeList, err := s.svc.ListActive(r.Context()); err == nil {
		for _, p := range activeList {
			if p.BaseAmount == req.Amount || p.Reference == req.OrderID {
				_, _ = s.svc.CancelPayment(r.Context(), p.ID)
			}
		}
	}

	pay, err := s.svc.CreatePayment(r.Context(), payment.CreatePaymentInput{
		Amount:    req.Amount,
		Reference: req.OrderID,
		ExpiresIn: time.Duration(expiryMinutes) * time.Minute,
		Metadata:  metadata,
	})
	if err != nil {
		s.logger.Error(fmt.Sprintf("CreatePayment failed: %v", err), nil)
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	if req.CallbackURL != "" {
		s.mu.Lock()
		s.callbacks[pay.ID] = req.CallbackURL
		s.mu.Unlock()
	}

	var qrB64 string
	if qrCode, err := qr.Encode(pay.QRString, qr.M); err == nil {
		qrB64 = "data:image/png;base64," + base64.StdEncoding.EncodeToString(qrCode.PNG())
	}

	res := CreatePaymentResponse{
		Success:      true,
		PaymentID:    pay.ID,
		OrderID:      pay.Reference,
		BaseAmount:   pay.BaseAmount,
		UniqueAmount: pay.UniqueAmount,
		UniqueOffset: pay.UniqueOffset,
		Status:       string(pay.Status),
		QRISString:   pay.QRString,
		QRISImageB64: qrB64,
		ExpiresAt:    pay.ExpiresAt,
		CreatedAt:    pay.CreatedAt,
	}

	jsonResponse(w, http.StatusCreated, res)
}

func (s *Server) handleGetPayment(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/payments/")
	if id == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "payment ID diperlukan"})
		return
	}

	pay, err := s.svc.GetPayment(r.Context(), id)
	if err != nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "pembayaran tidak ditemukan"})
		return
	}

	res := PaymentStatusResponse{
		Success:      true,
		PaymentID:    pay.ID,
		OrderID:      pay.Reference,
		UniqueAmount: pay.UniqueAmount,
		Status:       string(pay.Status),
		ExpiresAt:    pay.ExpiresAt,
	}

	if pay.Transaction != nil {
		res.PaidAt = &pay.Transaction.Time
		res.TxID = pay.Transaction.ID
		res.PaymentType = pay.Transaction.PaymentType
	}

	jsonResponse(w, http.StatusOK, res)
}

func (s *Server) handlePaymentPaid(p core.Payment) {
	s.logger.Info(fmt.Sprintf("🎉 PEMBAYARAN DITERIMA: %s (Order: %s, Rp %d)", p.ID, p.Reference, p.UniqueAmount), nil)

	s.mu.RLock()
	callbackURL, exists := s.callbacks[p.ID]
	s.mu.RUnlock()

	if !exists && p.Metadata != nil {
		if u, ok := p.Metadata["callback_url"].(string); ok {
			callbackURL = u
		}
	}

	if callbackURL == "" {
		return
	}

	go func(targetURL string, payment core.Payment) {
		payload := map[string]any{
			"event":          "payment.success",
			"payment_id":     payment.ID,
			"order_id":       payment.Reference,
			"amount":         payment.BaseAmount,
			"paid_amount":    payment.UniqueAmount,
			"status":         string(payment.Status),
			"transaction_id": "",
			"paid_at":        time.Now().Format(time.RFC3339),
		}
		if payment.Transaction != nil {
			payload["transaction_id"] = payment.Transaction.ID
			payload["paid_at"] = payment.Transaction.Time.Format(time.RFC3339)
			payload["payment_type"] = payment.Transaction.PaymentType
		}

		bodyBytes, _ := json.Marshal(payload)
		client := &http.Client{Timeout: 10 * time.Second}

		for attempt := 1; attempt <= 3; attempt++ {
			req, err := http.NewRequest(http.MethodPost, targetURL, bytes.NewBuffer(bodyBytes))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "QRISPaymentGateway/1.0")

			resp, err := client.Do(req)
			if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
				resp.Body.Close()
				s.logger.Info(fmt.Sprintf("✅ Webhook berhasil dikirim ke %s", targetURL), nil)
				return
			}
			if resp != nil {
				resp.Body.Close()
			}
			s.logger.Warn(fmt.Sprintf("Webhook attempt %d failed: %v", attempt, err), nil)
			time.Sleep(time.Duration(attempt*2) * time.Second)
		}
		s.logger.Error(fmt.Sprintf("❌ Gagal mengirim webhook ke %s setelah 3 percobaan", targetURL), nil)
	}(callbackURL, p)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func jsonResponse(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
