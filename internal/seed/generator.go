package seed

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/eabugauch/zenithpay-retry/internal/domain"
)

var (
	softDeclineCodes = []string{
		"insufficient_funds",
		"issuer_timeout",
		"do_not_honor",
		"processor_error",
		"authentication_failed",
	}
	hardDeclineCodes = []string{
		"stolen_card",
		"fraud_suspected",
		"invalid_card",
		"expired_card",
	}
	// Soft decline weights (approximately 70% of total)
	softWeights = []float64{0.30, 0.20, 0.25, 0.15, 0.10}

	currencies = []string{"USD", "BRL", "MXN", "COP", "PEN"}
	processors = []string{"stripe_latam", "adyen_apac", "dlocal_br", "payu_mx", "mercadopago_co"}
	merchants  = []string{"voltcommerce", "megastore_br", "shopfast_mx"}
)

// GenerateTransactions creates a realistic dataset of failed transactions.
func GenerateTransactions(count int, seed int64) []domain.SubmitRequest {
	rng := rand.New(rand.NewSource(seed))
	transactions := make([]domain.SubmitRequest, 0, count)

	now := time.Now().UTC()
	sevenDaysAgo := now.Add(-7 * 24 * time.Hour)

	softCount := int(float64(count) * 0.70)
	hardCount := count - softCount

	for i := 0; i < softCount; i++ {
		code := weightedChoice(rng, softDeclineCodes, softWeights)
		tx := generateTransaction(rng, i+1, code, sevenDaysAgo, now)
		transactions = append(transactions, tx)
	}

	hardWeights := []float64{0.25, 0.25, 0.25, 0.25}
	for i := 0; i < hardCount; i++ {
		code := weightedChoice(rng, hardDeclineCodes, hardWeights)
		tx := generateTransaction(rng, softCount+i+1, code, sevenDaysAgo, now)
		transactions = append(transactions, tx)
	}

	rng.Shuffle(len(transactions), func(i, j int) {
		transactions[i], transactions[j] = transactions[j], transactions[i]
	})

	return transactions
}

func generateTransaction(rng *rand.Rand, idx int, declineCode string, start, end time.Time) domain.SubmitRequest {
	duration := end.Sub(start)
	randomOffset := time.Duration(rng.Int63n(int64(duration)))
	timestamp := start.Add(randomOffset)

	amount := 10.0 + rng.Float64()*990.0 // $10 - $1000
	amount = float64(int(amount*100)) / 100

	currency := currencies[rng.Intn(len(currencies))]
	processor := processors[rng.Intn(len(processors))]
	merchant := merchants[rng.Intn(len(merchants))]
	customerID := fmt.Sprintf("cust_%06d", rng.Intn(5000)+1)

	return domain.SubmitRequest{
		TransactionID:     fmt.Sprintf("txn_%06d", idx),
		Amount:            amount,
		Currency:          currency,
		CustomerID:        customerID,
		MerchantID:        merchant,
		OriginalProcessor: processor,
		DeclineCode:       declineCode,
		Timestamp:         timestamp.Format(time.RFC3339),
	}
}

func weightedChoice(rng *rand.Rand, items []string, weights []float64) string {
	total := 0.0
	for _, w := range weights {
		total += w
	}

	r := rng.Float64() * total
	cumulative := 0.0
	for i, w := range weights {
		cumulative += w
		if r <= cumulative {
			return items[i]
		}
	}
	return items[len(items)-1]
}
