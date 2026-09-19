#!/usr/bin/env bash
set -euo pipefail
# Run against dedicated, empty test databases. The upgrade databases are built
# from the actual latest released source, without changing any worktree.
: "${VIDEO_TEST_MYSQL_DSN:?set a dedicated fresh MySQL test DSN}"
: "${VIDEO_TEST_POSTGRES_DSN:?set a dedicated fresh PostgreSQL test DSN}"
: "${VIDEO_UPGRADE_MYSQL_DSN:?set an empty MySQL upgrade fixture DSN}"
: "${VIDEO_UPGRADE_POSTGRES_DSN:?set an empty PostgreSQL upgrade fixture DSN}"
video_repo_root=$(git rev-parse --show-toplevel)
video_release=${VIDEO_BASELINE_RELEASE:-v1.5}
video_fixture_root=$(mktemp -d /tmp/video-release-matrix-XXXXXX)
trap 'rm -rf "$video_fixture_root"' EXIT
export VIDEO_UPGRADE_SQLITE_PATH="$video_fixture_root/released.sqlite"
mkdir "$video_fixture_root/source"
git -C "$video_repo_root" archive "$video_release" | tar -x -C "$video_fixture_root/source"
cat > "$video_fixture_root/source/model/video_acceptance_baseline_test.go" <<'GO'
package model

import (
 "os"
 "testing"
 "github.com/QuantumNous/new-api/common"
 "github.com/stretchr/testify/require"
)

func TestVideoAcceptanceReleasedFixture(t *testing.T){
 for _,tc:=range []struct{name,dsn,path string}{
  {"sqlite","local",os.Getenv("VIDEO_UPGRADE_SQLITE_PATH")},
  {"mysql",os.Getenv("VIDEO_UPGRADE_MYSQL_DSN"),""},
  {"postgres",os.Getenv("VIDEO_UPGRADE_POSTGRES_DSN"),""},
 }{
  t.Run(tc.name,func(t *testing.T){
   common.SQLitePath=tc.path
   t.Setenv("VIDEO_RELEASE_FIXTURE_DSN",tc.dsn)
   db,kind,err:=chooseDB("VIDEO_RELEASE_FIXTURE_DSN",false)
   require.NoError(t,err)
   DB,LOG_DB=db,db
   common.SetDatabaseTypes(kind,kind)
   initCol()
   require.False(t,db.Migrator().HasTable("users"),"upgrade fixture must start in an empty dedicated database")
   require.NoError(t,migrateDB())
   require.NoError(t,db.Create(&User{Id:700001,Username:"video_acceptance_preserved",Status:1,Group:"default",Quota:7654321}).Error)
   require.NoError(t,db.Create(&Token{Id:700001,UserId:700001,Key:"videoacceptancepreservedkey",Status:1,ExpiredTime:-1,RemainQuota:12345}).Error)
   require.NoError(t,db.Create(&Channel{Id:700001,Name:"released video acceptance",Type:1,Status:1,Models:"kimi-k3",Group:"default"}).Error)
   require.NoError(t,db.Create(&Option{Key:"video_acceptance_preserved",Value:"中文 preserved setting"}).Error)
   require.NoError(t,db.Create(&AIRouterLog{Id:700001,RequestId:"released-video-fixture",UserId:700001,Model:"kimi-k3",Modality:"text",Success:true,CreatedAt:1700000000}).Error)
   require.NoError(t,migrateDB())
   manifest:=map[string]any{}
   for _,table:=range []string{"users","tokens","channels","options","ai_router_logs"}{
    var rows []map[string]any
    tx:=db.Table(table)
    if table=="options"{tx=tx.Where(map[string]any{"key":"video_acceptance_preserved"})}else{tx=tx.Where("id = ?",700001)}
    require.NoError(t,tx.Find(&rows).Error)
    data,err:=common.Marshal(rows)
    require.NoError(t,err)
    indexes,err:=db.Migrator().GetIndexes(table)
    require.NoError(t,err)
    names:=[]string{}
    for _,index:=range indexes{names=append(names,index.Name())}
    manifest[table]=map[string]any{"rows":string(data),"indexes":names}
   }
   encoded,err:=common.Marshal(manifest)
   require.NoError(t,err)
   require.NoError(t,db.Create(&Option{Key:"video_acceptance_manifest",Value:string(encoded)}).Error)
   sqlDB,err:=db.DB()
   require.NoError(t,err)
   require.NoError(t,sqlDB.Close())
  })
 }
}
GO
(
 cd "$video_fixture_root/source"
 go test ./model -run '^TestVideoAcceptanceReleasedFixture$' -count=1 -v
)
cd "$video_repo_root"
go test ./model -run '^TestVideoPreprocess(Database|ReleasedUpgrade)$' -count=1 -v
