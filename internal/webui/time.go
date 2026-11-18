package webui

import "time"

// time0 disables Last-Modified on index.html (embedded files have no mtime).
var time0 = time.Time{}
