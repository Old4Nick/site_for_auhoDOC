-- Same boundary whitespace characters as Go strings.TrimSpace (Unicode White_Space).
CREATE FUNCTION trim_boundary_space(text) RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$ SELECT btrim($1, U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000') $$;

CREATE TABLE equipment_types (
    name text PRIMARY KEY,
    CONSTRAINT equipment_types_name_valid CHECK (char_length(name) BETWEEN 1 AND 200 AND name=trim_boundary_space(name))
);
CREATE UNIQUE INDEX equipment_types_normalized_unique ON equipment_types(lower(btrim(name)));

CREATE TABLE assets (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    equipment_type text NOT NULL REFERENCES equipment_types(name),
    model text NOT NULL CHECK (char_length(model) BETWEEN 1 AND 500 AND model=trim_boundary_space(model)),
    inventory_number text NOT NULL CHECK (char_length(inventory_number) BETWEEN 1 AND 200 AND inventory_number=trim_boundary_space(inventory_number)),
    serial_number text CHECK (serial_number IS NULL OR (char_length(serial_number) BETWEEN 1 AND 200 AND serial_number=trim_boundary_space(serial_number))),
    received_date date CHECK (received_date IS NULL OR received_date BETWEEN DATE '0001-01-01' AND DATE '9999-12-31'),
    current_holder text CHECK (current_holder IS NULL OR (char_length(current_holder) BETWEEN 1 AND 500 AND current_holder=trim_boundary_space(current_holder))),
    price_minor bigint NOT NULL CHECK (price_minor>=0),
    vat_mode text NOT NULL CHECK (vat_mode IN ('included','none')),
    vat_rate numeric(5,2),
    vat_minor bigint NOT NULL CHECK (vat_minor>=0 AND vat_minor<=price_minor),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT assets_vat_mode_valid CHECK (
        (vat_mode='none' AND vat_rate IS NULL AND vat_minor=0)
        OR (vat_mode='included' AND vat_rate IS NOT NULL AND vat_rate>=0 AND vat_rate<=999.99)
    )
);
CREATE UNIQUE INDEX assets_inventory_number_normalized_unique ON assets(lower(btrim(inventory_number)));
CREATE INDEX assets_type_normalized ON assets(lower(btrim(equipment_type)));

CREATE FUNCTION assets_set_updated_at() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := GREATEST(clock_timestamp(), OLD.updated_at + INTERVAL '1 microsecond');
    RETURN NEW;
END $$;
CREATE TRIGGER assets_updated_at BEFORE UPDATE ON assets FOR EACH ROW EXECUTE FUNCTION assets_set_updated_at();
