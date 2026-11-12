package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_RUNTIME_URL"))
	if err != nil {
		panic(err)
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `
select tablename || ' | ' || indexname || ' | ' || indexdef
from pg_indexes
where schemaname = 'public'
  and tablename in ('inventoryItem','servicePart','settlement','settlementPart')
order by tablename, indexname`)
	if err != nil {
		panic(err)
	}
	defer rows.Close()
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			panic(err)
		}
		fmt.Println(line)
	}
}
