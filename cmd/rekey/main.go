// Command rekey re-encrypts the stored service secrets from an old SECRET_KEY
// to a new one. Run it with OLD_SECRET, NEW_SECRET and DATABASE_URL set.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"kenosm/internal/crypto"
	"kenosm/internal/db"
)

func main() {
	oldSecret := os.Getenv("OLD_SECRET")
	newSecret := os.Getenv("NEW_SECRET")
	dbURL := os.Getenv("DATABASE_URL")
	if oldSecret == "" || newSecret == "" || dbURL == "" {
		log.Fatal("set OLD_SECRET, NEW_SECRET and DATABASE_URL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := db.NewPool(ctx, dbURL)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	oldC, err := crypto.New(oldSecret)
	if err != nil {
		log.Fatal(err)
	}
	newC, err := crypto.New(newSecret)
	if err != nil {
		log.Fatal(err)
	}

	rows, err := pool.Query(ctx, `SELECT id, upstream_api_key, proxy_password FROM services`)
	if err != nil {
		log.Fatalf("query: %v", err)
	}

	type rec struct {
		id   string
		key  string
		pass string
	}
	var recs []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.id, &r.key, &r.pass); err != nil {
			log.Fatalf("scan: %v", err)
		}
		recs = append(recs, r)
	}
	rows.Close()

	for _, r := range recs {
		plainKey, err := oldC.Decrypt(r.key)
		if err != nil {
			log.Fatalf("decrypt key for %s with OLD_SECRET failed: %v (is OLD_SECRET correct?)", r.id, err)
		}
		plainPass, err := oldC.Decrypt(r.pass)
		if err != nil {
			log.Fatalf("decrypt password for %s with OLD_SECRET failed: %v", r.id, err)
		}
		newKey, err := newC.Encrypt(plainKey)
		if err != nil {
			log.Fatal(err)
		}
		newPass, err := newC.Encrypt(plainPass)
		if err != nil {
			log.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE services SET upstream_api_key = $2, proxy_password = $3 WHERE id = $1`, r.id, newKey, newPass); err != nil {
			log.Fatalf("update %s: %v", r.id, err)
		}
		fmt.Printf("re-keyed service %s\n", r.id)
	}
	fmt.Printf("done: %d service(s) re-keyed\n", len(recs))
}
