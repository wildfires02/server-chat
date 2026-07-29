package server

import (
	_ "chat/server/auth/anon"
	_ "chat/server/auth/basic"
	_ "chat/server/auth/code"
	_ "chat/server/auth/rest"
	_ "chat/server/auth/token"
	_ "chat/server/db/mongodb"
	_ "chat/server/db/mysql"
	_ "chat/server/db/postgres"
	_ "chat/server/db/rethinkdb"
	_ "chat/server/media/fs"
	_ "chat/server/media/s3"
	_ "chat/server/push/fcm"
	_ "chat/server/push/stdout"
	_ "chat/server/push/tnpg"
	_ "chat/server/validate/email"
	_ "chat/server/validate/tel"
)
