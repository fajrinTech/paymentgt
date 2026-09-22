package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hirotomasato/paygateme/core"
	"github.com/hirotomasato/paygateme/payment"
	"github.com/hirotomasato/paygateme/shopee"
	"github.com/hirotomasato/paygateme/utils"
	"rsc.io/qr"
)

func main() {
	sessionPath := flag.String("session", "session.json", "path to saved session JSON")
	amount := flag.Int64("amount", 1000, "nominal pembayaran (rupiah, misal: 1000 atau 10000)")
	// Masukkan string QRIS statis merchant (format: 00020101...) via flag -qris atau env STATIC_QRIS
	staticQris := flag.String("qris", os.Getenv("STATIC_QRIS"), "payload QRIS statis (atau set env STATIC_QRIS)")
	flag.Parse()

	if *staticQris == "" {
		fmt.Fprintln(os.Stderr, "Error: QRIS statis diperlukan. Gunakan flag -qris \"<string_qris>\" atau set env STATIC_QRIS.")
		os.Exit(1)
	}

	data, err := os.ReadFile(*sessionPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Gagal membaca %s: %v\nPastikan session.json telah dikonfigurasi (lihat session.example.json).\n", *sessionPath, err)
		os.Exit(1)
	}

	var session shopee.Session
	if err := json.Unmarshal(data, &session); err != nil {
		fmt.Fprintf(os.Stderr, "Format %s tidak valid: %v\n", *sessionPath, err)
		os.Exit(1)
	}

	logger := utils.NewConsoleLogger(utils.LevelInfo)
	// Mode nominal bulat murni / pas (offset 0 selalu)
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

	// Pastikan sesi masih valid
	if _, err := provider.RefreshSession(ctx); err != nil {
		fmt.Printf("Peringatan refresh session: %v (mencoba lanjut...)\n", err)
	}

	svc, err := provider.Payments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Gagal inisialisasi payment service: %v\n", err)
		os.Exit(1)
	}

	orderRef := fmt.Sprintf("DEMO-%d", time.Now().Unix()%10000)
	fmt.Printf("\n==================================================\n")
	fmt.Printf("🛒 MEMBUAT TRANSAKSI PEMBAYARAN\n")
	fmt.Printf("==================================================\n")
	fmt.Printf("Merchant : %s (Store ID: %s)\n", session.Merchant.Name, session.StoreID)
	fmt.Printf("Order Ref: %s\n", orderRef)

	pay, err := svc.CreatePayment(ctx, payment.CreatePaymentInput{
		Amount:    *amount,
		Reference: orderRef,
		ExpiresIn: 10 * time.Minute,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Gagal membuat pembayaran: %v\n", err)
		os.Exit(1)
	}

	if pay.UniqueOffset == 0 {
		fmt.Printf("💰 TOTAL HARUS DIBAYAR: Rp %d (Nominal Murni Pas)\n", pay.UniqueAmount)
	} else {
		fmt.Printf("💰 TOTAL HARUS DIBAYAR: Rp %d (Kode unik: +%d)\n", pay.UniqueAmount, pay.UniqueOffset)
	}
	fmt.Printf("⏳ Kadaluarsa : %s\n", pay.ExpiresAt.Format("15:04:05"))

	qrObj, err := qr.Encode(pay.QRString, qr.M)
	if err == nil {
		_ = os.WriteFile("qris_bayar.png", qrObj.PNG(), 0o644)
		fmt.Println("🖼️  Gambar QRIS disimpan ke: qris_bayar.png (bisa dibuka langsung)")
	}

	fmt.Println("\n👇 SCAN QRIS DI BAWAH INI (ATAU BUKA qris_bayar.png):")
	renderTerminalQR(pay.QRString)

	fmt.Printf("\n📡 Menunggu pembayaran Rp %d masuk ke rekening ShopeePay...\n", pay.UniqueAmount)
	fmt.Println("Tekan Ctrl+C untuk membatalkan.")

	paidChan := make(chan core.Payment, 1)
	svc.OnPaid(func(p core.Payment) {
		if p.ID == pay.ID {
			paidChan <- p
		}
	})

	svc.OnExpired(func(p core.Payment) {
		if p.ID == pay.ID {
			fmt.Printf("\n❌ Transaksi %s telah kadaluarsa.\n", p.ID)
			cancel()
		}
	})

	svc.Start()
	defer svc.Stop()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	select {
	case p := <-paidChan:
		fmt.Println("\n🎉 ===============================================")
		fmt.Printf("✅ PEMBAYARAN BERHASIL DITERIMA!\n")
		fmt.Printf("Order ID        : %s\n", p.Reference)
		fmt.Printf("Nominal Diterima: Rp %d\n", p.UniqueAmount)
		if p.Transaction != nil {
			fmt.Printf("Transaction ID  : %s\n", p.Transaction.ID)
			fmt.Printf("Payment Type    : %s\n", p.Transaction.PaymentType)
			fmt.Printf("Waktu Bayar     : %s\n", p.Transaction.Time.Format("15:04:05"))
		}
		fmt.Println("🎉 ===============================================")
	case <-sigChan:
		fmt.Println("\nDibatalkan oleh user.")
	case <-ctx.Done():
	}
}

// renderTerminalQR mencetak QR ke terminal menggunakan blok ANSI
func renderTerminalQR(text string) {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		fmt.Println("(Gagal render ASCII QR, silakan buka file qris_bayar.png)")
		return
	}

	size := code.Size
	// Border / Quiet Zone
	quiet := 1

	for y := -quiet; y < size+quiet; y += 2 {
		for x := -quiet; x < size+quiet; x++ {
			top := false
			bottom := false

			if x >= 0 && x < size && y >= 0 && y < size {
				top = code.Black(x, y)
			}
			if x >= 0 && x < size && y+1 >= 0 && y+1 < size {
				bottom = code.Black(x, y+1)
			}

			// Menggunakan half-block unicode: ▀, ▄, █, ' '
			// Dark terminal: latar belakang hitam, blok putih
			if !top && !bottom {
				fmt.Print("█") // dua-duanya putih
			} else if top && bottom {
				fmt.Print(" ") // dua-duanya hitam
			} else if top && !bottom {
				fmt.Print("▄") // atas hitam, bawah putih
			} else {
				fmt.Print("▀") // atas putih, bawah hitam
			}
		}
		fmt.Println()
	}
}
