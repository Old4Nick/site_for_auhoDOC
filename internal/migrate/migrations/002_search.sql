CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;
CREATE INDEX assets_equipment_type_trgm ON assets USING gin(equipment_type gin_trgm_ops);
CREATE INDEX assets_model_trgm ON assets USING gin(model gin_trgm_ops);
CREATE INDEX assets_inventory_number_trgm ON assets USING gin(inventory_number gin_trgm_ops);
CREATE INDEX assets_serial_number_trgm ON assets USING gin(serial_number gin_trgm_ops);
