-- SYNTHESIZED, not a captured session. Built from OpenCode's source at
-- github.com/anomalyco/opencode@893e8dc43f05e872d4cac603486ce8a7181170fa:
-- the table definitions are packages/core/src/session/sql.ts (session_v2 and
-- session_message as OpenCode 2.1.2 creates them, session_part from
-- packages/core/src/database/migration/20261029002625_session_part.ts), and
-- the row data follows packages/schema/src/session-message.ts (assistant
-- messages and their tool parts) and the edit, write and patch tools in
-- packages/core/src/tool/plugin/. {{repo}} is the repository under test.
CREATE TABLE `session_v2` (
  `id` text PRIMARY KEY,
  `project_id` text NOT NULL,
  `workspace_id` text,
  `parent_id` text,
  `fork_session_id` text,
  `fork_boundary` text,
  `slug` text NOT NULL,
  `directory` text NOT NULL,
  `path` text,
  `title` text,
  `version` text NOT NULL,
  `share_url` text,
  `summary_additions` integer,
  `summary_deletions` integer,
  `summary_files` integer,
  `summary_diffs` text,
  `metadata` text,
  `cost` real DEFAULT 0 NOT NULL,
  `tokens_input` integer DEFAULT 0 NOT NULL,
  `tokens_output` integer DEFAULT 0 NOT NULL,
  `tokens_reasoning` integer DEFAULT 0 NOT NULL,
  `tokens_cache_read` integer DEFAULT 0 NOT NULL,
  `tokens_cache_write` integer DEFAULT 0 NOT NULL,
  `revert` text,
  `permission` text,
  `agent` text,
  `model` text,
  `time_created` integer NOT NULL,
  `time_updated` integer NOT NULL,
  `time_idle` integer,
  `time_viewed` integer,
  `idle_outcome` text,
  `time_compacting` integer,
  `time_archived` integer,
  `time_suspended` integer,
  `resume_attempts` integer DEFAULT 0 NOT NULL
);
CREATE TABLE `session_message` (
  `id` text PRIMARY KEY,
  `session_id` text NOT NULL,
  `type` text NOT NULL,
  `seq` integer NOT NULL,
  `time_created` integer NOT NULL,
  `time_updated` integer NOT NULL,
  `data` text NOT NULL,
  CONSTRAINT `fk_session_message_session_id_session_v2_id_fk` FOREIGN KEY (`session_id`) REFERENCES `session_v2`(`id`) ON DELETE CASCADE
);
CREATE TABLE `session_part` (
  `message_id` text NOT NULL,
  `session_id` text NOT NULL,
  `ordinal` integer NOT NULL,
  `type` text NOT NULL,
  `call_id` text,
  `time_created` integer NOT NULL,
  `time_updated` integer NOT NULL,
  `data` text NOT NULL,
  CONSTRAINT `session_part_pk` PRIMARY KEY(`message_id`, `ordinal`),
  CONSTRAINT `fk_session_part_message_id_session_message_id_fk` FOREIGN KEY (`message_id`) REFERENCES `session_message`(`id`) ON DELETE CASCADE,
  CONSTRAINT `fk_session_part_session_id_session_v2_id_fk` FOREIGN KEY (`session_id`) REFERENCES `session_v2`(`id`) ON DELETE CASCADE
);

-- The session, a subagent's session under it, and an unrelated session.
INSERT INTO session_v2 (id, project_id, parent_id, slug, directory, version, agent, model, time_created, time_updated) VALUES
  ('ses_0a1b2c3d4e5fRootSessionAAAA', 'prj_1', NULL, 'brave-otter', '{{repo}}', '2.2.5', 'build', '{"id":"claude-sonnet-5-5","providerID":"anthropic","variant":"high"}', 1793000000000, 1793000600000),
  ('ses_0a1b2c3d4e5fChildSessionBBB', 'prj_1', 'ses_0a1b2c3d4e5fRootSessionAAAA', 'calm-heron', '{{repo}}', '2.2.5', 'explore', NULL, 1793000100000, 1793000200000),
  ('ses_0a1b2c3d4e5fOtherSessionCCC', 'prj_1', NULL, 'quiet-lynx', '{{repo}}', '2.2.5', 'build', NULL, 1793000000000, 1793000600000);

INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data) VALUES
  ('msg_01', 'ses_0a1b2c3d4e5fRootSessionAAAA', 'user', 1, 1793000000000, 1793000000000,
   '{"text":"Add a and c","files":[],"agents":[],"skills":[],"time":{"created":1793000000000}}'),
  -- A step whose parts are inline in its content: a write by absolute path,
  -- an edit by a path relative to the session directory, a failed edit, a
  -- read, a write still running, and a write outside the repository.
  ('msg_02', 'ses_0a1b2c3d4e5fRootSessionAAAA', 'assistant', 2, 1793000001000, 1793000009000,
   '{"agent":"build","model":{"id":"claude-sonnet-5-5","providerID":"anthropic","variant":"high"},"content":[
     {"type":"text","text":"Writing the files."},
     {"type":"tool","id":"call_w1","name":"write","executed":true,"state":{"status":"completed","input":{"path":"{{repo}}/a.go","content":"package a\n"},"content":[{"type":"text","text":"Created file successfully: a.go"}],"metadata":{"truncated":false}},"time":{"created":1793000001100,"ran":1793000001200,"completed":1793000001300}},
     {"type":"tool","id":"call_e1","name":"edit","executed":true,"state":{"status":"completed","input":{"path":"pkg/c.go","oldString":"x","newString":"y"},"content":[{"type":"text","text":"Edited pkg/c.go (1 replacement)"}],"metadata":{"files":[{"file":"pkg/c.go","patch":"@@ -1 +1 @@\n-x\n+y\n","additions":1,"deletions":1,"status":"modified"}]}},"time":{"created":1793000002100,"ran":1793000002200,"completed":1793000002300}},
     {"type":"tool","id":"call_e2","name":"edit","executed":true,"state":{"status":"error","input":{"path":"b.go","oldString":"missing","newString":"y"},"error":{"type":"tool","message":"Could not find oldString in b.go."}},"time":{"created":1793000003100,"ran":1793000003200,"completed":1793000003300}},
     {"type":"tool","id":"call_r1","name":"read","executed":true,"state":{"status":"completed","input":{"path":"{{repo}}/r.go"},"content":[{"type":"text","text":"package r"}]},"time":{"created":1793000004100,"completed":1793000004300}},
     {"type":"tool","id":"call_w2","name":"write","state":{"status":"running","input":{"path":"d.go","content":"package d\n"},"metadata":{}},"time":{"created":1793000005100,"ran":1793000005200}},
     {"type":"tool","id":"call_w3","name":"write","executed":true,"state":{"status":"completed","input":{"path":"/elsewhere/x.go","content":"package x\n"},"content":[{"type":"text","text":"Created file successfully: /elsewhere/x.go"}]},"time":{"created":1793000006100,"completed":1793000006300}}
   ],"finish":"tool-calls","time":{"created":1793000001000,"completed":1793000009000}}'),
  -- A step whose parts are in session_part, at the provider's default
  -- variant, which names no effort.
  ('msg_03', 'ses_0a1b2c3d4e5fRootSessionAAAA', 'assistant', 3, 1793000010000, 1793000019000,
   '{"agent":"build","model":{"id":"gpt-6-luna","providerID":"openai","variant":"default"},"content":[],"finish":"stop","time":{"created":1793000010000,"completed":1793000019000}}'),
  ('msg_04', 'ses_0a1b2c3d4e5fRootSessionAAAA', 'idle', 4, 1793000020000, 1793000020000,
   '{"outcome":"succeeded","time":{"created":1793000020000}}'),
  -- The subagent's own model, with no variant.
  ('msg_05', 'ses_0a1b2c3d4e5fChildSessionBBB', 'assistant', 1, 1793000100000, 1793000109000,
   '{"agent":"explore","model":{"id":"claude-haiku-5-5","providerID":"anthropic"},"content":[
     {"type":"tool","id":"call_w4","name":"write","executed":true,"state":{"status":"completed","input":{"path":"{{repo}}/a.go","content":"package a\n\nconst A = 1\n"},"content":[{"type":"text","text":"Wrote file successfully: a.go"}]},"time":{"created":1793000100100,"completed":1793000100300}}
   ],"finish":"stop","time":{"created":1793000100000,"completed":1793000109000}}'),
  -- Another session's edit.
  ('msg_06', 'ses_0a1b2c3d4e5fOtherSessionCCC', 'assistant', 1, 1793000000000, 1793000009000,
   '{"agent":"build","model":{"id":"claude-opus-5-5","providerID":"anthropic","variant":"max"},"content":[
     {"type":"tool","id":"call_w5","name":"write","executed":true,"state":{"status":"completed","input":{"path":"{{repo}}/z.go","content":"package z\n"},"content":[{"type":"text","text":"Created file successfully: z.go"}]},"time":{"created":1793000000100,"completed":1793000000300}}
   ],"finish":"stop","time":{"created":1793000000000,"completed":1793000009000}}');

-- msg_03's parts: a patch that moved one file, added one and deleted one, and
-- a patch that failed.
INSERT INTO session_part (message_id, session_id, ordinal, type, call_id, time_created, time_updated, data) VALUES
  ('msg_03', 'ses_0a1b2c3d4e5fRootSessionAAAA', 0, 'tool', 'call_p1', 1793000011000, 1793000012000,
   '{"type":"tool","id":"call_p1","name":"patch","executed":true,"state":{"status":"completed","input":{"patchText":"*** Begin Patch\n*** Update File: pkg/old.go\n*** Move to: pkg/moved.go\n*** Add File: e.go\n*** Delete File: gone.go\n*** End Patch"},"content":[{"type":"text","text":"Success. Updated the following files:\nM pkg/moved.go\nA e.go\nD gone.go"}],"metadata":{"files":[{"file":"pkg/moved.go","patch":"","additions":0,"deletions":0,"status":"modified"},{"file":"e.go","patch":"","additions":1,"deletions":0,"status":"added"},{"file":"gone.go","patch":"","additions":0,"deletions":1,"status":"deleted"}]}},"time":{"created":1793000011000,"ran":1793000011100,"completed":1793000012000}}'),
  ('msg_03', 'ses_0a1b2c3d4e5fRootSessionAAAA', 1, 'tool', 'call_p2', 1793000013000, 1793000014000,
   '{"type":"tool","id":"call_p2","name":"patch","executed":true,"state":{"status":"error","input":{"patchText":"*** Begin Patch\n*** Update File: f.go\n*** End Patch"},"error":{"type":"tool","message":"Unable to apply patch"}},"time":{"created":1793000013000,"completed":1793000014000}}');
