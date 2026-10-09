CREATE TABLE `migrated` (
	`id` integer PRIMARY KEY NOT NULL,
	`name` text NOT NULL
);
--> statement-breakpoint
INSERT INTO `migrated` (`id`, `name`) VALUES (1, 'from a migration');
