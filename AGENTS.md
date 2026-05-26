# Project Conventions

## 数据库迁移规范 (Database Migration Rules)

### 1. 已提交的 schema 文件禁止编辑

已经提交到版本控制的 `migrations/*.sql` 文件**绝对不能编辑**。编辑已提交的迁移文件是严重的违规操作，会导致：
- 已有数据库的 schema 版本不一致
- 迁移系统的幂等性被破坏
- 生产环境部署故障

**正确做法**：始终创建**新的迁移文件**，通过增量 SQL 修改 schema。

### 2. 禁止使用 `xxx_new` 表重建模式

`CREATE TABLE xxx_new ... INSERT INTO xxx_new SELECT ... DROP TABLE xxx; ALTER TABLE xxx_new RENAME TO xxx` 这种表重建迁移模式是**违规的**。

在能够直接修改字段的情况下（如通过 `PRAGMA writable_schema` 修改 sqlite_master），**不允许**使用迁移表重建的方式。

**正确做法**：
- SQLite：使用 `PRAGMA writable_schema = ON; UPDATE sqlite_master SET sql = replace(...); PRAGMA writable_schema = OFF; PRAGMA schema_version = <N+1>;` 原地修改 schema
- 其他数据库：使用对应的 `ALTER TABLE ... ALTER COLUMN` / `DROP CONSTRAINT` 等 DDL 语句
