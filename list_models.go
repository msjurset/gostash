package main

import (
	"fmt"
	"io"
	"net/http"
	"github.com/msjurset/gostash/internal/credentials"
)

func main() {
	key, _ := credentials.Load(credentials.KeyGeminiAPIKey)
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models?key=%s", key)
	resp, _ := http.Get(url)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fmt.Println(string(body))
}
