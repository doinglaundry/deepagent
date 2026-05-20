package gateway

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"eino-cli/backend/runtime"
	"eino-cli/backend/uploads"
)

// handleUploadCreate stores the multipart "file" field under the thread's uploads dir.
func (s *Server) handleUploadCreate(c *gin.Context) {
	tid := c.Param("tid")
	uid := runtime.GetEffectiveUserID(c.Request.Context())

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer file.Close()

	dest, err := uploads.Write(tid, uid, header.Filename, file)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"path":         dest,
		"filename":     header.Filename,
		"virtual_path": "/mnt/user-data/uploads/" + header.Filename,
	})
}

// handleUploadList lists files in the thread's uploads dir.
func (s *Server) handleUploadList(c *gin.Context) {
	tid := c.Param("tid")
	uid := runtime.GetEffectiveUserID(c.Request.Context())
	files, err := uploads.List(tid, uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"files": files})
}

// handleUploadDelete removes one file with traversal/symlink guards.
func (s *Server) handleUploadDelete(c *gin.Context) {
	tid := c.Param("tid")
	uid := runtime.GetEffectiveUserID(c.Request.Context())
	name := c.Param("name")
	if err := uploads.Delete(tid, uid, name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}
