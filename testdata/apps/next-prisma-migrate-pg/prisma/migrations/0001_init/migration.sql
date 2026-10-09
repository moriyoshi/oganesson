CREATE TABLE "Widget" (
    "id" SERIAL NOT NULL,
    "name" TEXT NOT NULL,
    CONSTRAINT "Widget_pkey" PRIMARY KEY ("id")
);
INSERT INTO "Widget" ("name") VALUES ('alpha');
INSERT INTO "Widget" ("name") VALUES ('beta');
