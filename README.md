# PaymentGT

Gateway QRIS Dinamis & Settlement Daemon untuk Merchant ShopeePay berbasis Go.

PaymentGT mengubah QRIS Statis merchant ShopeePay menjadi **QRIS Dinamis** dengan nominal pas (tanpa kode unik jika diinginkan), memonitor mutasi transaksi secara real-time via feed settlement merchant, dan mengirim webhook HTTP secara instan ketika pembayaran sukses.

> ⚠️ **Catatan**: Project ini adalah implementasi independen untuk integrasi merchant ShopeePay. Pastikan penggunaan mematuhi ketentuan layanan yang berlaku.

---

## Fitur Utama

- **QRIS Dinamis EMVCo**: Generate payload QRIS dan gambar QR base64 secara instan dengan tag amount (Tag 54) dan CRC16 valid.
- **Nominal Pas / Murni**: Dukungan penuh untuk pembayaran nominal bulat murni tanpa kode unik acak.
- **Realisasi Settlement Cepat**: Daemon background polling feed mutasi ShopeePay secara berkala dan otomatis mencocokkan pembayaran.
- **Webhook Notifikasi**: Kirim HTTP POST callback otomatis ke backend/aplikasi Anda saat transaksi berstatus `PAID` (dengan retry otomatis 3x).
- **RESTful API**: Endpoint siap pakai untuk pembuatan pembayaran (`POST /api/payments`), pengecekan status (`GET /api/payments/{id}`), dan health check (`GET /health`).
- **Session Auto-Refresh**: Memperbarui token sesi merchant secara berkala untuk menjaga koneksi tetap aktif.

---

## Struktur Direktori

```
.
├── cmd/
│   ├── server/       # HTTP REST API server & webhook daemon
│   ├── gateway/      # CLI generator & terminal QR viewer
│   └── login/        # CLI interaktif login OTP ShopeePay Merchant
├── core/             # Tipe domain, status pembayaran, error handling
├── payment/          # Payment service, allocation manager, mutation matcher
├── qris/             # Parser & builder EMVCo QRIS, CRC16 checksum
├── shopee/           # Client API ShopeePay Partner/Merchant, auth & feed
├── utils/            # Logger, formatting helper, ID generator
├── session.example.json # Template konfigurasi session merchant
├── go.mod
└── README.md
```

---

## Cara Penggunaan

### 1. Ekstrak Sesi Merchant ShopeePay

Siapkan file `session.json` di root direktori. Anda bisa membuatnya melalui:

**Opsi A — Menggunakan CLI Login**:
```bash
go run ./cmd/login -save session.json
```
Masukkan nomor handphone yang terdaftar di Shopee Partner/Merchant dan masukkan kode OTP yang diterima.

**Opsi B — Menggunakan Cookie Browser**:
Salin file `session.example.json` menjadi `session.json`:
```bash
cp session.example.json session.json
```
Isi nilai cookie (`SPC_F`, `SPC_SEC_SI`, dsb) dan ID Merchant / Store ID dari cookie browser setelah login ke portal Shopee Partner.

### 2. Dapatkan String QRIS Statis

Dapatkan string QRIS statis outlet Anda (bisa scan QRIS cetak Anda menggunakan aplikasi QR scanner atau ambil dari aplikasi Shopee Partner). Format QRIS berawal dari `00020101...`.

### 3. Menjalankan REST API Server

Jalankan server menggunakan flag `-qris`:
```bash
go run ./cmd/server -port 8080 -qris "0002010102112661..."
```
Atau menggunakan environment variable `STATIC_QRIS`:
```bash
export STATIC_QRIS="0002010102112661..."
go run ./cmd/server -port 8080
```

Parameter server yang tersedia:
- `-port` : Port HTTP server (default: `8080`).
- `-session` : Lokasi file session JSON (default: `session.json`).
- `-qris` : Payload QRIS statis merchant (atau via env `STATIC_QRIS`).

---

## REST API Reference

### 1. Health Check
```http
GET /health
```
**Response**:
```json
{
  "status": "healthy",
  "store_id": "YOUR_STORE_ID",
  "merchant": "YOUR_STORE_NAME",
  "timestamp": "2026-09-22T08:00:00Z"
}
```

### 2. Buat Transaksi Pembayaran
```http
POST /api/payments
Content-Type: application/json
```
**Request Body**:
```json
{
  "order_id": "INV-20260922-001",
  "amount": 25000,
  "expires_in_minutes": 10,
  "callback_url": "https://api.domainanda.com/webhook/payment"
}
```
**Response**:
```json
{
  "success": true,
  "payment_id": "pay_xxxxxxxxxxxx",
  "order_id": "INV-20260922-001",
  "amount": 25000,
  "unique_amount": 25000,
  "unique_offset": 0,
  "status": "PENDING",
  "qris_string": "00020101021226...5405250005802ID...6304ABCD",
  "qris_image_base64": "data:image/png;base64,iVBORw0KGgo...",
  "expires_at": "2026-09-22T08:10:00Z",
  "created_at": "2026-09-22T08:00:00Z"
}
```

### 3. Cek Status Pembayaran
```http
GET /api/payments/{payment_id}
```
**Response (Setelah Dibayar)**:
```json
{
  "success": true,
  "payment_id": "pay_xxxxxxxxxxxx",
  "order_id": "INV-20260922-001",
  "unique_amount": 25000,
  "status": "PAID",
  "expires_at": "2026-09-22T08:10:00Z",
  "paid_at": "2026-09-22T08:03:15Z",
  "transaction_id": "TRX-SHOPEE-99881122",
  "payment_type": "SHOPEEPAY"
}
```

### 4. Format Payload Webhook
Ketika pembayaran terdeteksi lunas di feed ShopeePay, server akan mengirim POST request ke `callback_url`:
```json
{
  "event": "payment.paid",
  "payment_id": "pay_xxxxxxxxxxxx",
  "order_id": "INV-20260922-001",
  "amount": 25000,
  "status": "PAID",
  "transaction_id": "TRX-SHOPEE-99881122",
  "paid_at": "2026-09-22T08:03:15Z"
}
```

---

## Deployment (Systemd Service)

Contoh file unit `/etc/systemd/system/paymentgt.service` di server Linux:

```ini
[Unit]
Description=PaymentGT ShopeePay QRIS Gateway
After=network.target

[Service]
Type=simple
User=appuser
WorkingDirectory=/opt/paymentgt
Environment="STATIC_QRIS=0002010102112661..."
ExecStart=/opt/paymentgt/server -port 8080 -session /opt/paymentgt/session.json
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

---

## Lisensi

[MIT License](LICENSE)