package server

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"
)

func writeSSE(c *gin.Context, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, data)
	c.Writer.Flush()
}
