// Command hello is the smallest Torge application.
package main

import (
	"log"

	"github.com/TosmimForidMehtab/torge"
)

func main() {
	app := torge.New()

	app.GET("/", func(c *torge.Context) error {
		return c.JSON(200, map[string]string{"message": "hello"})
	})

	// ":$PORT" when PORT is set (Render, Heroku, Cloud Run...), else ":8080".
	if err := app.Listen(""); err != nil {
		log.Fatal(err)
	}
}
