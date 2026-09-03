package licensing

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// RequireFeature guards relay routes while keeping the normal request path to
// one in-memory read. No provider request or filesystem access occurs here.
func RequireFeature(feature string) gin.HandlerFunc {
	return func(c *gin.Context) {
		manager := Global()
		if manager == nil {
			c.Header("Retry-After", "60")
			c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{"error": gin.H{
				"type":    "license_required",
				"code":    "license_not_initialized",
				"message": "CPA license is inactive. Complete authorization in the management panel.",
			}})
			return
		}
		allowed, reason := manager.Check(feature)
		if allowed {
			c.Next()
			return
		}
		c.Header("Retry-After", "60")
		c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{
			"error": gin.H{
				"type":    "license_required",
				"code":    "license_" + reason,
				"message": "CPA 授权未生效，请在管理后台完成激活或续期。",
			},
		})
	}
}
