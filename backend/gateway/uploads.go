package gateway

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"eino-cli/backend/runtime"
	"eino-cli/backend/uploads"
)

// handleUploadCreate consumes a multipart "file" field and stores it
// under the thread's uploads dir. Returns the host path + safe name so
// clients can hand the LLM a /mnt/user-data/uploads/<safe_name> reference.
func (s *Server) handleUploadCreate(c *gin.Context) {
	tid := c.Param("tid")
	uid := runtime.GetEffectiveUserID(c.Request.Context())

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	defer file.Close()

	dest, err := uploads.Write(s.cfg, tid, uid, header.Filename, file)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"path":        dest,
		"filename":    header.Filename,
		"virtual_path": "/mnt/user-data/uploads/" + header.Filename,
	})
}

// handleUploadList: enumerate files in the thread's uploads dir. Returns
// the same FileInfo shape uploads.List produces.
func (s *Server) handleUploadList(c *gin.Context) {
	tid := c.Param("tid")
	uid := runtime.GetEffectiveUserID(c.Request.Context())
	files, err := uploads.List(s.cfg, tid, uid)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"files": files})
}

// handleUploadDelete drops a single file safely (no symlink traversal).
func (s *Server) handleUploadDelete(c *gin.Context) {
	tid := c.Param("tid")
	uid := runtime.GetEffectiveUserID(c.Request.Context())
	name := c.Param("name")
	if err := uploads.Delete(s.cfg, tid, uid, name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}
